package process

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"os"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/oauth"
)

// startHTTP starts an HTTP-based MCP server connection.
func (s *Supervisor) startHTTP(ctx context.Context, id InstanceID, generation uint64, srv config.ServerConfig) (*Handle, error) {
	name := id.Server
	log.Printf("Starting HTTP server: name=%s url=%s", name, srv.URL)

	// Emit starting event
	s.emitStatus(name, events.StateStarting, 0, nil, "")

	// Determine authentication
	var bearerToken string
	var bearerTokenProvider func(context.Context) (string, error)
	authStatus := mcp.AuthStatusNone

	// Check bearer token first (highest priority)
	if srv.BearerTokenEnvVar != "" {
		token := os.Getenv(srv.BearerTokenEnvVar)
		if token == "" {
			err := fmt.Errorf("bearer token env var %s is not set", srv.BearerTokenEnvVar)
			s.emitStatus(name, events.StateError, 0, nil, err.Error())
			return nil, err
		}
		bearerToken = token
		authStatus = mcp.AuthStatusBearer
	} else if s.tokenManager != nil {
		// Check for OAuth credentials
		log.Printf("Looking up OAuth token for URL: %s", srv.URL)
		token, err := s.tokenManager.GetAccessToken(ctx, srv.URL)
		if err == nil && token != "" {
			log.Printf("Found OAuth token for %s (len=%d)", name, len(token))
			bearerToken = token
			bearerTokenProvider = func(callCtx context.Context) (string, error) {
				return s.tokenManager.GetAccessToken(callCtx, srv.URL)
			}
			authStatus = mcp.AuthStatusOAuthOK
		} else {
			log.Printf("No OAuth token found for %s: err=%v", name, err)
			// Try to discover OAuth support
			metadata, _ := oauth.SupportsOAuth(ctx, srv.URL)
			if metadata != nil {
				authStatus = mcp.AuthStatusOAuthNeeds
				// Don't fail - server might work without auth, or user can login later
				log.Printf("Server %s supports OAuth but needs login", name)
			}
		}
	}

	// Build HTTP headers
	headers := make(map[string]string)
	maps.Copy(headers, srv.HTTPHeaders)
	for headerName, envVarName := range srv.EnvHTTPHeaders {
		if value := os.Getenv(envVarName); value != "" {
			headers[headerName] = value
		}
	}

	// Create HTTP transport
	transportConfig := mcp.StreamableHTTPConfig{
		URL:                 srv.URL,
		BearerToken:         bearerToken,
		BearerTokenProvider: bearerTokenProvider,
		HTTPHeaders:         headers,
	}
	httpTransport := mcp.NewStreamableHTTPTransport(transportConfig)

	// Connect SSE stream
	if err := httpTransport.Connect(ctx); err != nil {
		// Check if it's an auth error
		if authStatus == mcp.AuthStatusOAuthNeeds {
			log.Printf("Server %s requires OAuth login", name)
		}
		s.emitStatus(name, events.StateError, 0, nil, err.Error())
		return nil, fmt.Errorf("connect HTTP transport: %w", err)
	}

	// Create client
	client := mcp.NewClient(httpTransport)

	// Create handle and register under lock
	handleCtx, handleCancel := context.WithCancel(context.Background())
	handle := &Handle{
		id:            name,
		instance:      id,
		generation:    generation,
		kind:          HandleKindHTTP,
		ctx:           handleCtx,
		ctxCancel:     handleCancel,
		client:        client,
		httpTransport: httpTransport,
		authStatus:    authStatus,
		serverURL:     srv.URL,
		serverConfig:  srv,
		logs:          make([]string, 0, 1000),
		toolsReady:    make(chan struct{}),
		bus:           s.bus,
		startedAt:     time.Now(),
		done:          make(chan struct{}),
	}
	handle.onStopped = s.notifyInstanceStopped

	s.mu.Lock()
	s.handles[id] = handle
	s.mu.Unlock()

	// Initialize MCP connection
	initCtx, cancel := context.WithTimeout(ctx, time.Duration(srv.StartupTimeout())*time.Second)
	defer cancel()

	if err := client.Initialize(initCtx); err != nil {
		// Check if it's an auth error - we can handle this gracefully
		var unauthErr *mcp.UnauthorizedError
		if errors.As(err, &unauthErr) {
			log.Printf("Server %s returned 401, checking for OAuth support", name)

			// Try to discover OAuth via the challenge
			var oauthMeta *oauth.AuthorizationServerMetadata
			if unauthErr.Challenge != nil && unauthErr.Challenge.ResourceMetadata != "" {
				// Challenge is now *oauth.BearerChallenge, can use directly
				result, discErr := oauth.DiscoverFromChallenge(ctx, unauthErr.Challenge)
				if discErr == nil && result != nil {
					oauthMeta = result.Metadata
					log.Printf("Discovered OAuth via resource_metadata for %s", name)
				} else {
					log.Printf("Failed to discover OAuth from challenge: %v", discErr)
				}
			}

			// Fallback: try standard discovery
			if oauthMeta == nil {
				oauthMeta, _ = oauth.SupportsOAuth(ctx, srv.URL)
			}

			if oauthMeta != nil {
				// Server supports OAuth - put handle in "needs login" state.
				// The handle is already published, so publish the whole state
				// change atomically and close the transport afterwards.
				_ = handle.setNeedsLogin(oauthMeta, unauthErr.Challenge)
				_ = httpTransport.Close()
				needsLoginErr := fmt.Errorf("%w for server %s", ErrNeedsLogin, name)
				handle.setInitError(needsLoginErr)
				s.publishDiscovery(DiscoveryResult{
					Instance: handle.instance, Generation: handle.generation,
					Sequence: handle.NextDiscoverySequence(), Err: needsLoginErr,
				})
				handle.signalToolsReady()

				s.emitStatus(name, events.StateNeedsAuth, 0, nil, "OAuth login required")
				log.Printf("Server %s requires OAuth login", name)
				return handle, nil
			}
		}

		s.publishDiscovery(DiscoveryResult{
			Instance: handle.instance, Generation: handle.generation,
			Sequence: handle.NextDiscoverySequence(), Err: err,
		})
		_ = handle.Stop()
		s.emitStatus(name, events.StateError, 0, nil, fmt.Sprintf("MCP init failed: %v", err))
		return nil, fmt.Errorf("initialize mcp: %w", err)
	}

	// Install notification handler now that initialization succeeded.
	s.installNotificationHandler(handle, client)

	// Emit running event immediately (tool discovery happens in background)
	s.emitStatus(name, events.StateRunning, 0, nil, "")

	// Discover tools in background (non-blocking)
	go s.discoverToolsAsync(handle, client, name)

	return handle, nil
}
