package server

import (
	"fmt"
	"log"
	"slices"

	"github.com/Bigsy/mcpmu/internal/config"
)

// resolveNamespace determines which namespace to use and which servers are active.
func (s *Session) resolveNamespace() *RPCError {
	cfg := s.currentConfig()
	name, servers, method, rpcErr := resolveNamespaceSelection(cfg, s.opts.Namespace)
	if rpcErr != nil {
		return rpcErr
	}
	s.activeNamespaceName = name
	s.activeServerNames = servers
	s.selectionMethod = method
	switch method {
	case SelectionFlag:
		log.Printf("Using namespace %q with %d servers (selection: flag)", name, len(servers))
	case SelectionDefault:
		log.Printf("Using default namespace %q with %d servers (selection: default)", name, len(servers))
	case SelectionOnly:
		log.Printf("Using only namespace %q with %d servers (selection: only)", name, len(servers))
	case SelectionAll:
		log.Printf("No namespaces configured, exposing all %d enabled servers (selection: all)", len(servers))
	}
	return nil
}

func resolveNamespaceSelection(cfg *config.Config, namespaceArg string) (string, []string, SelectionMethod, *RPCError) {

	// Rule 1: If --namespace provided, use it (lookup by name)
	if namespaceArg != "" {
		if ns, exists := cfg.Namespaces[namespaceArg]; exists {
			return namespaceArg, slices.Clone(ns.ServerIDs), SelectionFlag, nil
		}
		return "", nil, "", ErrNamespaceNotFound(namespaceArg)
	}

	// Rule 2: If config.defaultNamespace is set, use it
	if cfg.DefaultNamespace != "" {
		if ns, exists := cfg.Namespaces[cfg.DefaultNamespace]; exists {
			return cfg.DefaultNamespace, slices.Clone(ns.ServerIDs), SelectionDefault, nil
		}
		return "", nil, "", ErrNamespaceNotFound(cfg.DefaultNamespace)
	}

	// Rule 3: If exactly 1 namespace, use it
	if len(cfg.Namespaces) == 1 {
		for name, ns := range cfg.Namespaces {
			return name, slices.Clone(ns.ServerIDs), SelectionOnly, nil
		}
	}

	// Rule 4: If 0 namespaces, expose all enabled servers
	if len(cfg.Namespaces) == 0 {
		servers := make([]string, 0, len(cfg.Servers))
		for name, srv := range cfg.Servers {
			if srv.IsEnabled() {
				servers = append(servers, name)
			}
		}
		slices.Sort(servers)
		return "", servers, SelectionAll, nil
	}

	// Rule 5: 2+ namespaces, none selected - fail
	return "", nil, "", NewRPCError(ErrCodeInvalidRequest,
		fmt.Sprintf("Multiple namespaces configured (%d), but none selected. Use --namespace to specify which namespace to expose.", len(cfg.Namespaces)),
		nil)
}

func (s *Session) applyReloadConfig(newCfg *config.Config) []string {
	return s.applyReloadConfigKeeping(newCfg, false)
}

func (s *Session) applyReloadConfigKeeping(newCfg *config.Config, keep bool) []string {
	s.mu.Lock()
	defer s.mu.Unlock()

	oldNamespaceName := s.activeNamespaceName
	oldSelectionMethod := s.selectionMethod

	// Re-resolve namespace
	// If namespace was selected by flag and still exists, keep it
	// If namespace was auto-selected and still valid, keep it
	// If namespace no longer exists, re-auto-select
	var keepNamespace bool
	if oldSelectionMethod == SelectionFlag && s.opts.Namespace != "" {
		// Try to find the namespace by the original flag value
		if ns, exists := newCfg.Namespaces[s.opts.Namespace]; exists {
			s.activeNamespaceName = s.opts.Namespace
			s.activeServerNames = slices.Clone(ns.ServerIDs)
			s.selectionMethod = SelectionFlag
			keepNamespace = true
		}
	} else if oldNamespaceName != "" && (newCfg.DefaultNamespace == "" || newCfg.DefaultNamespace == oldNamespaceName) {
		// Try to keep the same namespace by name
		if ns, exists := newCfg.Namespaces[oldNamespaceName]; exists {
			s.activeNamespaceName = oldNamespaceName
			s.activeServerNames = slices.Clone(ns.ServerIDs)
			s.selectionMethod = oldSelectionMethod
			keepNamespace = true
		}
	}

	if !keepNamespace {
		name, servers, method, err := resolveNamespaceSelection(newCfg, s.opts.Namespace)
		if err != nil {
			log.Printf("WARN: namespace resolution failed after reload, exposing no servers: %v", err)
			s.activeNamespaceName = oldNamespaceName
			s.activeServerNames = nil
			s.selectionMethod = oldSelectionMethod
		} else {
			s.activeNamespaceName = name
			s.activeServerNames = servers
			s.selectionMethod = method
		}
	} else {
		log.Printf("Kept namespace %q after reload with %d servers",
			s.activeNamespaceName, len(s.activeServerNames))
	}

	// Rebuild aggregator and router with new config. Swap under the write
	// lock so concurrently-running handlers see either the whole old pair or
	// the whole new pair, never a torn read.
	s.replacePrivateAggregatorKeeping(keep)
	newRouter := NewRouter(s)

	s.router = newRouter
	activeNsName := s.activeNamespaceName
	selMethod := s.selectionMethod

	newRouter.SetActiveNamespace(activeNsName, selMethod)
	if !s.opts.EagerStart {
		return nil
	}
	return slices.Clone(s.activeServerNames)
}
