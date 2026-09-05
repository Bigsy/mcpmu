package server

import (
	"maps"
	"reflect"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
)

// metadataOnlyReload is deliberately an allowlist. Unknown/new config fields
// retain full-reload behavior until their readers and lifecycle have been audited.
func metadataOnlyReload(old, next *config.Config) bool {
	normalize := func(cfg *config.Config) config.Config {
		c := *cfg
		c.LastModified = time.Time{}
		c.ToolPermissions = nil
		c.Servers = maps.Clone(c.Servers)
		for name, srv := range c.Servers {
			srv.DeniedTools = nil
			c.Servers[name] = srv
		}
		c.Namespaces = maps.Clone(c.Namespaces)
		for name, ns := range c.Namespaces {
			ns.Description = ""
			ns.Compression = ""
			ns.DenyByDefault = false
			ns.ServerDefaults = nil
			c.Namespaces[name] = ns
		}
		return c
	}
	return reflect.DeepEqual(normalize(old), normalize(next))
}

// selectiveReload permits server and namespace edits; all global fields retain
// the existing full-reload path (including metrics/OAuth recorder lifecycle).
func selectiveReload(old, next *config.Config) (map[string]bool, bool) {
	normalize := func(cfg *config.Config) config.Config {
		c := *cfg
		c.Servers = nil
		c.Namespaces = nil
		c.ToolPermissions = nil
		c.DefaultNamespace = ""
		c.LastModified = time.Time{}
		return c
	}
	if !reflect.DeepEqual(normalize(old), normalize(next)) {
		return nil, false
	}
	changed := map[string]bool{}
	for name, srv := range old.Servers {
		other, exists := next.Servers[name]
		if !exists || !reflect.DeepEqual(runtimeServerConfig(srv), runtimeServerConfig(other)) {
			changed[name] = true
		}
	}
	return changed, true
}

func runtimeServerConfig(s config.ServerConfig) config.ServerConfig {
	s.DeniedTools = nil
	isHTTP := s.IsHTTP()
	s.Kind = config.ServerKindStdio
	if isHTTP {
		s.Kind = config.ServerKindStreamableHTTP
	}
	s.Enabled = new(s.IsEnabled())
	s.Shared = new(s.IsShared())
	s.StartupTimeoutSec = s.StartupTimeout()
	s.ToolTimeoutSec = s.ToolTimeout()
	if len(s.Args) == 0 {
		s.Args = nil
	}
	if len(s.Env) == 0 {
		s.Env = nil
	}
	if len(s.HTTPHeaders) == 0 {
		s.HTTPHeaders = nil
	}
	if len(s.EnvHTTPHeaders) == 0 {
		s.EnvHTTPHeaders = nil
	}
	return s
}
