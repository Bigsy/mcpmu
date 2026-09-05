package server

import (
	"context"
	"slices"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/process"
)

// applySelectiveReload publishes the acquisition barrier before retiring any
// identities. Catalogs survive, with generation tombstones only for retired
// instances. Visibility is updated before private cleanup; subscription cleanup
// never holds a Core/Session lock across upstream I/O.
func (c *Core) applySelectiveReload(ctx context.Context, cfg *config.Config, initiator *Session, changed map[string]bool) {
	aggregator := NewAggregator(cfg, c.supervisor, false)
	aggregator.acquire = c.getOrStartHandle
	c.coreMu.Lock()
	aggregator.catalog = c.aggregator.catalog
	c.cfg = cfg
	c.aggregator = aggregator
	c.configGeneration++
	c.retiring = changed
	c.coreMu.Unlock()
	sessions := c.sessionSnapshot()
	if initiator != nil && !slices.Contains(sessions, initiator) {
		sessions = append(sessions, initiator)
	}
	for _, s := range sessions {
		s.applyReloadConfigKeeping(cfg, true)
	}
	retire := func(id process.InstanceID) bool {
		if changed[id.Server] {
			return true
		}
		return !id.IsShared() && !c.privateInstanceAllowed(id)
	}
	// Drop revoked intents immediately; unsubscribe retained upstreams in a
	// worker, under the existing per-key lock. Retirement closes affected ones.
	c.pruneReloadSubscriptions(ctx, retire)
	c.supervisor.StopMatching(retire, func(id process.InstanceID, generation uint64) {
		if a := c.aggregatorForInstance(id); a != nil {
			a.catalog.retire(id, generation)
		}
	})
	for _, s := range sessions {
		s.resourceMapMu.Lock()
		for uri, id := range s.resourceMap {
			if changed[id.Server] || !s.instanceVisible(id) {
				delete(s.resourceMap, uri)
			}
		}
		s.resourceMapMu.Unlock()
	}
	// Keep replacements blocked until every session has discarded old routes.
	c.coreMu.Lock()
	c.retiring = nil
	c.coreMu.Unlock()
	for _, s := range sessions {
		if s.opts.EagerStart {
			s.spawn("eager start after selective reload", func(lifetime context.Context) error {
				startCtx, cancel := joinContext(ctx, lifetime)
				defer cancel()
				s.startEagerServers(startCtx)
				return nil
			})
		}
		s.sendNotification("notifications/tools/list_changed")
		if s.opts.ExposeResources {
			s.sendNotification("notifications/resources/list_changed")
		}
		if s.opts.ExposePrompts {
			s.sendNotification("notifications/prompts/list_changed")
		}
	}
}

func (s *Session) instanceVisible(id process.InstanceID) bool {
	s.mu.RLock()
	selected := slices.Contains(s.activeServerNames, id.Server)
	s.mu.RUnlock()
	srv, exists := s.currentConfig().GetServer(id.Server)
	return selected && exists && srv.IsEnabled() && srv.IsShared() == id.IsShared() && (id.IsShared() || id.Session == s.id)
}
func (c *Core) privateInstanceAllowed(id process.InstanceID) bool {
	if id.IsShared() {
		return true
	}
	session := c.sessionForID(id.Session)
	if session == nil || session.closed.Load() {
		return false
	}
	session.mu.RLock()
	selected := slices.Contains(session.activeServerNames, id.Server)
	initialized := session.initialized
	session.mu.RUnlock()
	if !initialized {
		_, names, _, err := resolveNamespaceSelection(c.currentConfig(), session.opts.Namespace)
		return err == nil && slices.Contains(names, id.Server)
	}
	return selected
}
func (c *Core) subscriptionAllowed(s *Session, key resourceSubscriptionKey, generation uint64) bool {
	c.coreMu.RLock()
	retiring := c.retiring[key.Instance.Server]
	c.coreMu.RUnlock()
	if retiring || !s.instanceVisible(key.Instance) {
		return false
	}
	h := c.supervisor.GetInstance(key.Instance)
	return h != nil && h.IsRunning() && h.Generation() == generation
}

func (c *Core) pruneReloadSubscriptions(ctx context.Context, retire func(process.InstanceID) bool) {
	r := c.subscriptions
	r.mu.Lock()
	var orphaned []resourceSubscriptionKey
	for key, entry := range r.entries {
		for s := range entry.sessions {
			if retire(key.Instance) || !s.instanceVisible(key.Instance) {
				delete(entry.sessions, s)
				s.deleteSubscription(key)
			}
		}
		if len(entry.sessions) == 0 {
			delete(r.entries, key)
			if !retire(key.Instance) {
				orphaned = append(orphaned, key)
			}
		}
	}
	r.mu.Unlock()
	for _, key := range orphaned {
		c.spawn("release revoked subscription", func(lifetime context.Context) error {
			cleanupCtx, cancel := joinContext(ctx, lifetime)
			defer cancel()
			c.cleanupOrphanSubscription(cleanupCtx, key)
			return nil
		})
	}
}

func (c *Core) cleanupOrphanSubscription(ctx context.Context, key resourceSubscriptionKey) {
	r := c.subscriptions
	lock := r.lockKey(key)
	defer r.unlockKey(key, lock)
	r.mu.RLock()
	entry := r.entries[key]
	occupied := entry != nil && len(entry.sessions) > 0
	r.mu.RUnlock()
	if occupied {
		return
	}
	if h := c.supervisor.GetInstance(key.Instance); h != nil {
		_ = c.unsubscribeUpstream(ctx, key, h.Generation())
	}
}
