package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"sync"

	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
)

// handleResourcesList handles the resources/list request.
//
// No resourceStateMu hold across this handler: it starts upstreams (up to
// StartupTimeout) and issues RPCs, and a global read lock here starved hot
// reload and Core.Close daemon-wide. Every structure touched below
// synchronizes on its own lock (s.mu, resourceMapMu, instance lifecycle), and
// the subscription table's epoch check discards work a concurrent reload
// invalidates.
func (s *Session) handleResourcesList(ctx context.Context) (any, *RPCError) {
	// Snapshot for the install guard at the bottom: a list that gathered its
	// routing table under the old config must not land after a reload wiped
	// this state.
	genAtEntry := s.currentConfigGeneration()

	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()

	type listedResource struct {
		URI         string          `json:"uri"`
		Name        string          `json:"name"`
		Title       string          `json:"title,omitempty"`
		Description string          `json:"description,omitempty"`
		MimeType    string          `json:"mimeType,omitempty"`
		Size        *int64          `json:"size,omitempty"`
		Annotations json.RawMessage `json:"annotations,omitempty"`
		Icons       json.RawMessage `json:"icons,omitempty"`
		Meta        json.RawMessage `json:"_meta,omitempty"`
	}

	type serverResources struct {
		resources []mcp.Resource
	}
	results := make([]serverResources, len(activeServerNames))
	sem := make(chan struct{}, MaxConcurrentDiscovery)
	var wg sync.WaitGroup

	for index, name := range activeServerNames {
		if !s.aggregatorForServer(name).shouldQueryCapability(name, catalogResources) {
			continue
		}
		wg.Add(1)
		resultIndex, serverName := index, name
		goSafe("resources/list "+name, func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			sc, rpcErr := s.getOrStartServer(ctx, serverName)
			if rpcErr != nil {
				log.Printf("Failed to get client for %s (resources/list): %v", serverName, rpcErr)
				return
			}

			callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
			defer cancel()

			resources, err := sc.client.ListResources(callCtx)
			if err != nil {
				log.Printf("Failed to list resources from %s: %v", serverName, err)
				return
			}

			results[resultIndex].resources = resources
		})
	}

	wg.Wait()

	allResources := make([]listedResource, 0)
	owners := make(map[string]process.InstanceID)
	for index, result := range results {
		serverName := activeServerNames[index]
		instance := s.instanceID(serverName)
		for _, r := range result.resources {
			if firstOwner, exists := owners[r.URI]; exists {
				log.Printf("resources/list URI collision for %q: keeping %s, omitting %s", r.URI, firstOwner, serverName)
				continue
			}
			owners[r.URI] = instance
			allResources = append(allResources, listedResource{
				URI:         r.URI,
				Name:        r.Name,
				Title:       r.Title,
				Description: r.Description,
				MimeType:    r.MimeType,
				Size:        r.Size,
				Annotations: r.Annotations,
				Icons:       r.Icons,
				Meta:        r.Meta,
			})
		}
	}
	// Install the routing table only if the config is still the one this list
	// ran against. The generation check and the write share resourceMapMu with
	// the reload's wipe of this session's state, and the reload bumps the
	// generation strictly before wiping — so either the install precedes the
	// wipe (and is cleaned up by it) or the check fails. A stale table cannot
	// survive the reload.
	s.resourceMapMu.Lock()
	if s.currentConfigGeneration() == genAtEntry {
		s.resourceMap = owners
	}
	s.resourceMapMu.Unlock()
	return struct {
		Resources []listedResource `json:"resources"`
	}{Resources: allResources}, nil
}

// handleResourcesRead handles the resources/read request. Like
// handleResourcesList, it holds no global lock across its upstream I/O.
func (s *Session) handleResourcesRead(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()

	var req struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}

	// Look up which server owns this URI (populated by resources/list)
	s.resourceMapMu.RLock()
	instance, ok := s.resourceMap[req.URI]
	s.resourceMapMu.RUnlock()
	if !ok {
		return nil, ErrInvalidParams("unknown resource URI (has resources/list been called?): " + req.URI)
	}
	if !slices.Contains(activeServerNames, instance.Server) {
		return nil, ErrServerNotFound(instance.Server)
	}

	sc, rpcErr := s.getOrStartServer(ctx, instance.Server)
	if rpcErr != nil {
		return nil, rpcErr
	}

	callCtx, cancel := context.WithTimeout(ctx, sc.timeout)
	defer cancel()

	contents, err := sc.client.ReadResource(callCtx, req.URI)
	if err != nil {
		return nil, ErrInternalError(fmt.Sprintf("resources/read from %s: %v", instance, err))
	}

	return struct {
		Contents json.RawMessage `json:"contents"`
	}{Contents: contents}, nil
}

// handleResourcesSubscribe handles the resources/subscribe request. The
// subscribe transition itself is serialized per key and epoch-checked inside
// resourceSubscriptions, so no global lock is held across the upstream RPC.
func (s *Session) handleResourcesSubscribe(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	activeServerNames := s.activeServerNames
	s.mu.RUnlock()

	var req struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if req.URI == "" {
		return nil, ErrInvalidParams("missing uri")
	}

	s.resourceMapMu.RLock()
	instance, ok := s.resourceMap[req.URI]
	s.resourceMapMu.RUnlock()
	if !ok {
		return nil, ErrInvalidParams("unknown resource URI (has resources/list been called?): " + req.URI)
	}
	if !slices.Contains(activeServerNames, instance.Server) {
		return nil, ErrServerNotFound(instance.Server)
	}

	sc, rpcErr := s.getOrStartServer(ctx, instance.Server)
	if rpcErr != nil {
		return nil, rpcErr
	}

	if sc.capabilities.Resources == nil || !sc.capabilities.Resources.Subscribe {
		return nil, ErrMethodNotFound(fmt.Sprintf("upstream %s does not support resources/subscribe", instance))
	}

	key := resourceSubscriptionKey{Instance: instance, URI: req.URI}
	if err := s.subscribeResource(ctx, s, key, sc); err != nil {
		return nil, ErrInternalError(fmt.Sprintf("resources/subscribe on %s: %v", instance, err))
	}

	return struct{}{}, nil
}

// handleResourcesUnsubscribe handles the resources/unsubscribe request.
// Unknown URIs are treated as idempotent success — clients often unsubscribe
// defensively, and the URI may have been evicted by a concurrent resources/list.
func (s *Session) handleResourcesUnsubscribe(ctx context.Context, params json.RawMessage) (any, *RPCError) {
	s.mu.RLock()
	if !s.initialized {
		s.mu.RUnlock()
		return nil, ErrInvalidRequest("not initialized")
	}
	s.mu.RUnlock()

	var req struct {
		URI string `json:"uri"`
	}
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, ErrInvalidParams(err.Error())
	}
	if req.URI == "" {
		return nil, ErrInvalidParams("missing uri")
	}

	// Prefer s.subs for lookup (client may unsubscribe after a list refresh
	// evicted resourceMap); fall back to resourceMap.
	s.subMu.Lock()
	instance, known := s.subs[req.URI]
	s.subMu.Unlock()
	if !known {
		s.resourceMapMu.RLock()
		mappedInstance, ok := s.resourceMap[req.URI]
		s.resourceMapMu.RUnlock()
		if ok {
			instance = mappedInstance
			known = true
		}
	}
	if !known {
		// Idempotent: client cleanup on an unknown URI is not an error.
		return struct{}{}, nil
	}

	// Unsubscribe is always a local success. The Core sends an upstream RPC
	// only for the 1→0 transition and logs (rather than surfacing) any failure.
	// If the namespace changed, the same removal path skips a dead/missing
	// upstream while still clearing retained intent.
	s.unsubscribeResource(ctx, s, resourceSubscriptionKey{Instance: instance, URI: req.URI})

	return struct{}{}, nil
}
