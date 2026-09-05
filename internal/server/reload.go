package server

import (
	"context"
	"log"
	"slices"

	"github.com/Bigsy/mcpmu/internal/config"
)

// watchConfig watches the config file for changes and sends new config to reloadCh.
// It watches the parent directory (not the file) to handle atomic renames.
func (c *Core) watchConfig(ctx context.Context, configPath string) {
	report := func(err error) { log.Printf("%v", err) }
	err := config.Watch(ctx, configPath, c.debounceDelay, func(cfg *config.Config) {
		select {
		case c.reloadCh <- cfg:
		case <-ctx.Done():
		}
	}, report)
	if err != nil {
		report(err)
	}
}

// applyReload applies a new configuration once at Core scope, then re-resolves
// every attached Session. Embedded mode has one Session; daemon mode's
// Core-owned watcher calls this directly for all live Sessions.
func (s *Session) applyReload(ctx context.Context, newCfg *config.Config) {
	s.Core.applyReload(ctx, newCfg, s)
}

func (c *Core) applyReload(ctx context.Context, newCfg *config.Config, initiator *Session) {
	if metadataOnlyReload(c.currentConfig(), newCfg) {
		// Authorization and compression read Core's immutable config per request.
		// Runtime settings and namespace membership are identical: catalogs,
		// process generations, subscriptions and in-flight discovery remain valid.
		c.coreMu.Lock()
		c.cfg = newCfg
		c.coreMu.Unlock()
		sessions := c.sessionSnapshot()
		if initiator != nil && !slices.Contains(sessions, initiator) {
			sessions = append(sessions, initiator)
		}
		for _, session := range sessions {
			session.sendNotification("notifications/tools/list_changed")
		}
		return
	}

	if changed, ok := selectiveReload(c.currentConfig(), newCfg); ok {
		c.applySelectiveReload(ctx, newCfg, initiator, changed)
		return
	}
	// resourceStateMu excludes only the other writer (Core.Close): handlers
	// never take it, because they run upstream I/O that must not stall a
	// reload. The clearing below is internally synchronized; the epoch bump
	// inside subscriptions.clear() invalidates any operation still in flight
	// against the old generation.
	//
	// Ordering matters twice over. The generation advance must precede the
	// clearing: handleResourcesList guards its resourceMap install with an
	// entry-time generation snapshot, and "generation unchanged" implying
	// "not yet wiped" is what makes that guard sound. The clearing itself
	// must precede StopAll: closing the upstream transport ends the
	// upstream-side subscription cleanly, so only local bookkeeping is
	// dropped here — no per-URI unsubscribe RPC is attempted, it would race
	// with shutdown.
	log.Printf("Applying config reload: %d servers, %d namespaces",
		len(newCfg.Servers), len(newCfg.Namespaces))

	// Advance the config generation before stopping instances. Any stale
	// get-or-start path must revalidate under its instance lifecycle lock.
	c.replaceConfig(newCfg)

	c.resourceStateMu.Lock()
	c.clearResourceStateForReload()
	c.resourceStateMu.Unlock()

	// Stop all running servers after the generation barrier is visible.
	c.supervisor.StopAll()

	sessions := c.sessionSnapshot()
	if initiator != nil && !slices.Contains(sessions, initiator) {
		// A few focused tests construct a Session directly around a Core. Keep
		// the internal applyReload hook correct for that legacy arrangement.
		sessions = append(sessions, initiator)
	}
	eagerSessions := make([]*Session, 0, len(sessions))
	for _, session := range sessions {
		if len(session.applyReloadConfig(newCfg)) > 0 {
			eagerSessions = append(eagerSessions, session)
		}
	}

	for _, session := range eagerSessions {
		session.spawn("eager start after reload", func(lifetime context.Context) error {
			startCtx, cancel := joinContext(ctx, lifetime)
			defer cancel()
			session.startEagerServers(startCtx)
			return nil
		})
	}

	for _, session := range sessions {
		session.sendNotification("notifications/tools/list_changed")
		if session.opts.ExposeResources {
			session.sendNotification("notifications/resources/list_changed")
		}
		if session.opts.ExposePrompts {
			session.sendNotification("notifications/prompts/list_changed")
		}
	}

	log.Printf("Config reload complete")
}
