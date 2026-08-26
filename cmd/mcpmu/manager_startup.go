package main

import (
	"fmt"
	"io"
	"log"
	"os"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/process"
)

// managerRuntime is everything the two management surfaces (TUI and web)
// build before they diverge: a loaded config, the event bus, the tool cache
// and a Supervisor that owns this process's upstream children. Both surfaces
// used to assemble this by hand in near-identical blocks; startManager is the
// one copy.
type managerRuntime struct {
	cfg        *config.Config
	configPath string
	bus        *events.Bus
	toolCache  *config.ToolCache
	supervisor *process.Supervisor

	mgrLock *process.ManagerLock
	logFile *os.File
}

// startManager sets up debug logging, takes the per-config manager lock
// under owner ("tui" or "web"), loads the config at the root --config path
// (or the default), and builds the bus, tool cache and Supervisor. On success
// the caller must call Close once the surface has exited; on error nothing is
// left held.
func startManager(owner string, debug bool) (*managerRuntime, error) {
	rt := &managerRuntime{}
	rt.setupLogging(owner, debug)

	// The manager lock keeps a second TUI/web instance off the same config.
	mgrLock, err := process.NewManagerLock(configPath)
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("failed to create manager lock: %w", err)
	}
	if err := mgrLock.Acquire(owner); err != nil {
		rt.Close()
		return nil, err
	}
	rt.mgrLock = mgrLock

	// Resolve the config path once so every consumer (tool cache, watcher,
	// mutations) shares the same file identity.
	rt.configPath = configPath
	if rt.configPath == "" {
		rt.configPath, err = config.ConfigPath()
		if err != nil {
			rt.Close()
			return nil, fmt.Errorf("failed to resolve config path: %w", err)
		}
	}
	rt.cfg, err = config.LoadFrom(rt.configPath)
	if err != nil {
		rt.Close()
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	log.Printf("Loaded %d servers from config", len(rt.cfg.Servers))

	rt.bus = events.NewBus()
	rt.bus.Subscribe(logEvent)

	// Tool cache is co-located with the active config; a failure to create
	// it degrades to no caching rather than aborting startup.
	rt.toolCache, err = config.NewToolCache(rt.configPath)
	if err != nil {
		log.Printf("Warning: failed to create tool cache: %v", err)
	}

	// PIDFilePrefix labels this owner's registry of spawned children.
	rt.supervisor = process.NewSupervisorWithOptions(rt.bus, process.SupervisorOptions{
		CredentialStoreMode:     rt.cfg.MCPOAuthCredentialStore,
		GlobalOAuthCallbackPort: rt.cfg.MCPOAuthCallbackPort,
		PIDFilePrefix:           owner,
	})
	rt.supervisor.SetToolCache(rt.toolCache)
	return rt, nil
}

// setupLogging routes the standard logger to the debug file when debug is
// set and discards it otherwise: both surfaces own the terminal or a
// browser, so stderr chatter is noise.
func (rt *managerRuntime) setupLogging(owner string, debug bool) {
	if !debug {
		log.SetOutput(io.Discard)
		return
	}
	logFile, err := os.OpenFile("/tmp/mcpmu-debug.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		log.SetOutput(io.Discard)
		return
	}
	rt.logFile = logFile
	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("=== mcpmu %s starting (debug mode) ===", owner)
}

// Close stops every upstream child, closes the bus, releases the manager
// lock and closes the debug log, in that order. Safe to call on a partially
// constructed runtime.
func (rt *managerRuntime) Close() {
	if rt.supervisor != nil {
		log.Println("Stopping all servers...")
		rt.supervisor.StopAll()
	}
	if rt.bus != nil {
		rt.bus.Close()
	}
	if rt.mgrLock != nil {
		rt.mgrLock.Release()
	}
	if rt.logFile != nil {
		_ = rt.logFile.Close()
	}
}

// logEvent mirrors every bus event into the debug log.
func logEvent(e events.Event) {
	switch evt := e.(type) {
	case events.StatusChangedEvent:
		log.Printf("EVENT StatusChanged: server=%s old=%s new=%s err=%s",
			evt.ServerID(), evt.OldState, evt.NewState, evt.Status.Error)
	case events.LogReceivedEvent:
		log.Printf("EVENT Log: server=%s line=%s", evt.ServerID(), evt.Line)
	case events.ErrorEvent:
		log.Printf("EVENT Error: server=%s msg=%s err=%v", evt.ServerID(), evt.Message, evt.Err)
	case events.ToolsUpdatedEvent:
		log.Printf("EVENT ToolsUpdated: server=%s count=%d", evt.ServerID(), len(evt.Tools))
	}
}
