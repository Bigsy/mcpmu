package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Auth modes for ServerFormData.AuthMode.
const (
	AuthModeNone   = "none"
	AuthModeBearer = "bearer"
	AuthModeOAuth  = "oauth"
)

// ServerFormData is the surface-neutral shape of a server add/edit form. The
// TUI and web forms both reduce to this and hand it to BuildServerConfig, so
// the merge-with-existing rules (what an edit preserves, what it clears) live
// in exactly one place.
type ServerFormData struct {
	IsHTTP bool // true → URL/auth fields apply; false → Command/Args/Cwd apply

	// Stdio
	Command string
	Args    string // shell-words; see SplitArgs
	Cwd     string

	// HTTP
	URL string
	// AuthMode is AuthModeNone, AuthModeBearer or AuthModeOAuth. Empty means
	// "infer": bearer when BearerEnv is set, otherwise OAuth when any OAuth
	// field is set or the existing server already used OAuth, otherwise none.
	AuthMode          string
	BearerEnv         string
	HTTPHeaders       string // one "Name: Value" per line
	EnvHTTPHeaders    string // one "Name: ENV_VAR" per line
	OAuthClientID     string
	OAuthCallbackPort string // decimal; empty for none
	OAuthScopes       string // comma-separated

	// Common
	Env       map[string]string
	Enabled   *bool // nil → keep existing (true for a new server)
	Autostart bool
	// Timeouts in seconds; 0 means "use the default".
	StartupTimeoutSec int
	ToolTimeoutSec    int
}

// BuildServerConfig turns form input into a ServerConfig. When existing is
// non-nil the result starts from it, so fields the form does not expose
// (Shared, DeniedTools, OAuth.ClientSecret, ...) survive an edit. Fields that
// belong to the other transport are cleared, so switching stdio↔HTTP leaves no
// contradictory leftovers for Validate to reject.
//
// It returns an error for input that cannot be represented: malformed header
// lines, a header set in both header fields, an unterminated quote in Args, or
// a callback port that is not a valid TCP port.
func BuildServerConfig(form ServerFormData, existing *ServerConfig) (ServerConfig, error) {
	var srv ServerConfig
	if existing != nil {
		srv = *existing
	}

	if form.Enabled != nil {
		srv.SetEnabled(*form.Enabled)
	}
	srv.Autostart = form.Autostart
	srv.StartupTimeoutSec = form.StartupTimeoutSec
	srv.ToolTimeoutSec = form.ToolTimeoutSec

	if len(form.Env) > 0 {
		srv.Env = make(map[string]string, len(form.Env))
		for k, v := range form.Env {
			srv.Env[k] = v
		}
	} else {
		srv.Env = nil
	}

	if form.IsHTTP {
		srv.Kind = ServerKindStreamableHTTP
		srv.URL = strings.TrimSpace(form.URL)
		srv.Command = ""
		srv.Args = nil
		srv.Cwd = ""

		headers, err := ParseHeaderLines(form.HTTPHeaders)
		if err != nil {
			return srv, fmt.Errorf("custom headers: %w", err)
		}
		envHeaders, err := ParseHeaderLines(form.EnvHTTPHeaders)
		if err != nil {
			return srv, fmt.Errorf("headers from env vars: %w", err)
		}
		for name := range envHeaders {
			if _, dup := headers[name]; dup {
				return srv, fmt.Errorf("header %q is set by both custom headers and headers from env vars", name)
			}
		}
		srv.HTTPHeaders = headers
		srv.EnvHTTPHeaders = envHeaders

		bearerEnv := strings.TrimSpace(form.BearerEnv)
		mode := form.AuthMode
		if mode == "" {
			mode = inferAuthMode(form, bearerEnv, existing)
		}
		switch mode {
		case AuthModeBearer:
			srv.BearerTokenEnvVar = bearerEnv
			srv.OAuth = nil
		case AuthModeOAuth:
			oauth, err := buildOAuth(form, existing)
			if err != nil {
				return srv, err
			}
			srv.BearerTokenEnvVar = ""
			srv.OAuth = oauth
		default:
			srv.BearerTokenEnvVar = ""
			srv.OAuth = nil
		}
	} else {
		srv.Kind = ServerKindStdio
		srv.Command = strings.TrimSpace(form.Command)
		srv.Cwd = strings.TrimSpace(form.Cwd)
		args, err := SplitArgs(form.Args)
		if err != nil {
			return srv, fmt.Errorf("arguments: %w", err)
		}
		srv.Args = args

		// Everything HTTP-only is cleared: stdio servers reject these at the
		// config layer, and keeping them from an existing HTTP config would
		// fail Validate on save.
		srv.URL = ""
		srv.BearerTokenEnvVar = ""
		srv.HTTPHeaders = nil
		srv.EnvHTTPHeaders = nil
		srv.OAuth = nil
	}

	return srv, nil
}

func inferAuthMode(form ServerFormData, bearerEnv string, existing *ServerConfig) string {
	if bearerEnv != "" {
		return AuthModeBearer
	}
	hasOAuthInput := strings.TrimSpace(form.OAuthClientID) != "" ||
		strings.TrimSpace(form.OAuthCallbackPort) != "" ||
		strings.TrimSpace(form.OAuthScopes) != ""
	if hasOAuthInput || (existing != nil && existing.OAuth != nil) {
		return AuthModeOAuth
	}
	return AuthModeNone
}

// buildOAuth builds the OAuth block from form fields, carrying the client
// secret over from the existing config: no form exposes it, and an edit that
// dropped it would silently force a re-login (or break a confidential client).
func buildOAuth(form ServerFormData, existing *ServerConfig) (*OAuthConfig, error) {
	oauth := &OAuthConfig{
		ClientID: strings.TrimSpace(form.OAuthClientID),
	}
	if existing != nil && existing.OAuth != nil {
		oauth.ClientSecret = existing.OAuth.ClientSecret
	}

	for scope := range strings.SplitSeq(form.OAuthScopes, ",") {
		if scope = strings.TrimSpace(scope); scope != "" {
			oauth.Scopes = append(oauth.Scopes, scope)
		}
	}

	if portStr := strings.TrimSpace(form.OAuthCallbackPort); portStr != "" {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("oauth callback port %q: must be a number between 1 and 65535", portStr)
		}
		oauth.CallbackPort = &port
	}

	return oauth, nil
}
