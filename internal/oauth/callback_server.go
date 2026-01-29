package oauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
)

// CallbackResult holds the result of an OAuth callback.
type CallbackResult struct {
	// Code is the authorization code from the callback.
	Code string

	// State is the state parameter from the callback.
	State string

	// Error is the error code if authorization failed.
	Error string

	// ErrorDescription provides more detail about the error.
	ErrorDescription string
}

// CallbackServer is a local HTTP server that receives OAuth callbacks.
type CallbackServer struct {
	listener net.Listener
	server   *http.Server
	result   chan CallbackResult
	port     int
	mu       sync.Mutex
	started  bool
}

// NewCallbackServer creates a new callback server.
// If port is 0 or nil, a random available port is used.
func NewCallbackServer(port *int) (*CallbackServer, error) {
	listenPort := 0
	if port != nil {
		listenPort = *port
	}

	// Only bind to localhost for security
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", listenPort))
	if err != nil {
		return nil, fmt.Errorf("listen on port %d: %w", listenPort, err)
	}

	// Get the actual port (important when using port 0)
	actualPort := listener.Addr().(*net.TCPAddr).Port

	cs := &CallbackServer{
		listener: listener,
		result:   make(chan CallbackResult, 1),
		port:     actualPort,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", cs.handleCallback)

	cs.server = &http.Server{Handler: mux}

	return cs, nil
}

// Port returns the port the server is listening on.
func (s *CallbackServer) Port() int {
	return s.port
}

// RedirectURI returns the redirect URI to use for OAuth.
func (s *CallbackServer) RedirectURI() string {
	return fmt.Sprintf("http://127.0.0.1:%d/callback", s.port)
}

// Start begins serving HTTP requests.
func (s *CallbackServer) Start() error {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return fmt.Errorf("server already started")
	}
	s.started = true
	s.mu.Unlock()

	go s.server.Serve(s.listener)
	return nil
}

// Wait blocks until a callback is received or the context is cancelled.
func (s *CallbackServer) Wait(ctx context.Context) (*CallbackResult, error) {
	select {
	case result := <-s.result:
		return &result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// Stop shuts down the server.
func (s *CallbackServer) Stop() error {
	return s.server.Shutdown(context.Background())
}

// handleCallback processes the OAuth callback.
func (s *CallbackServer) handleCallback(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	result := CallbackResult{
		Code:             query.Get("code"),
		State:            query.Get("state"),
		Error:            query.Get("error"),
		ErrorDescription: query.Get("error_description"),
	}

	// Send result (non-blocking in case Wait isn't called)
	select {
	case s.result <- result:
	default:
	}

	// Show success/error page to user
	if result.Error != "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>MCP Studio - Authorization Failed</title></head>
<body style="font-family: sans-serif; padding: 40px; text-align: center;">
<h1>Authorization Failed</h1>
<p>Error: %s</p>
<p>%s</p>
<p>You can close this window.</p>
</body>
</html>`, result.Error, result.ErrorDescription)
		return
	}

	if result.Code == "" {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>MCP Studio - Error</title></head>
<body style="font-family: sans-serif; padding: 40px; text-align: center;">
<h1>Error</h1>
<p>No authorization code received.</p>
<p>You can close this window.</p>
</body>
</html>`)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<!DOCTYPE html>
<html>
<head><title>MCP Studio - Authorization Complete</title></head>
<body style="font-family: sans-serif; padding: 40px; text-align: center;">
<h1>✓ Authorization Complete</h1>
<p>You can close this window and return to MCP Studio.</p>
</body>
</html>`)
}
