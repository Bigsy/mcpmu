package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/process"
)

// Core owns state that can be shared by multiple MCP sessions: upstream
// processes, tool aggregation, configuration, hot-reload input, and the
// upstream notification entry point.
type Core struct {
	coreMu           sync.RWMutex
	cfg              *config.Config
	configGeneration uint64

	bus        *events.Bus
	supervisor *process.Supervisor
	aggregator *Aggregator

	notifications NotificationBroadcaster
	reloadCh      chan *config.Config
	configPath    string
	debounceDelay time.Duration
	watchOnce     sync.Once
	closeOnce     sync.Once
}

// NewCore constructs the shared server core without binding it to a client
// connection. Call NewSession to attach the single Phase 1 session.
func NewCore(opts Options) (*Core, error) {
	bus := events.NewBus()

	pidTrackerDir := opts.PIDTrackerDir
	if pidTrackerDir == "" && opts.ConfigPath != "" {
		pidTrackerDir = filepath.Dir(opts.ConfigPath)
	}

	supervisor := process.NewSupervisorWithOptions(bus, process.SupervisorOptions{
		CredentialStoreMode:     opts.Config.MCPOAuthCredentialStore,
		PIDTrackerDir:           pidTrackerDir,
		GlobalOAuthCallbackPort: opts.Config.MCPOAuthCallbackPort,
	})

	if opts.ConfigPath != "" {
		toolCache, err := config.NewToolCache(opts.ConfigPath)
		if err != nil {
			log.Printf("Warning: failed to initialize tool cache: %v", err)
		} else {
			supervisor.SetToolCache(toolCache)
		}
	}

	broadcaster := &singleSubscriberBroadcaster{}
	c := &Core{
		cfg:              opts.Config,
		configGeneration: 1,
		bus:              bus,
		supervisor:       supervisor,
		notifications:    broadcaster,
		reloadCh:         make(chan *config.Config, 1),
		configPath:       opts.ConfigPath,
		debounceDelay:    opts.DebounceDelay,
	}
	c.aggregator = NewAggregator(c.cfg, supervisor, false)
	c.aggregator.acquire = c.getOrStartHandle

	// Supervisor only knows about the Core broadcaster, never an individual
	// Session. This is the seam Phase 2B expands to real fanout.
	supervisor.SetNotificationSink(broadcaster)
	return c, nil
}

func (c *Core) currentConfig() *config.Config {
	c.coreMu.RLock()
	defer c.coreMu.RUnlock()
	return c.cfg
}

func (c *Core) currentAggregator() *Aggregator {
	c.coreMu.RLock()
	defer c.coreMu.RUnlock()
	return c.aggregator
}

func (c *Core) replaceConfig(cfg *config.Config) *Aggregator {
	aggregator := NewAggregator(cfg, c.supervisor, false)
	aggregator.acquire = c.getOrStartHandle
	c.coreMu.Lock()
	c.cfg = cfg
	c.aggregator = aggregator
	c.configGeneration++
	c.coreMu.Unlock()
	return aggregator
}

// serverClient is a ready upstream connection plus the immutable metadata a
// request needs after acquisition.
type serverClient struct {
	handle       *process.Handle
	client       *mcp.Client
	timeout      time.Duration
	capabilities mcp.ServerCapabilities
}

// getOrStartHandle is the one lazy-start/readiness path used by tools,
// resources, prompts, and discovery. It snapshots the complete server config,
// then revalidates it under the instance lifecycle lock before a start.
func (c *Core) getOrStartHandle(ctx context.Context, serverName string) (*process.Handle, config.ServerConfig, error) {
	c.coreMu.RLock()
	snapshotGeneration := c.configGeneration
	srv, ok := c.cfg.GetServer(serverName)
	c.coreMu.RUnlock()
	if !ok {
		return nil, config.ServerConfig{}, fmt.Errorf("server not found: %s", serverName)
	}
	if !srv.IsEnabled() {
		return nil, config.ServerConfig{}, fmt.Errorf("server is disabled: %s", serverName)
	}
	snapshot, err := normalizedServerConfig(srv)
	if err != nil {
		return nil, config.ServerConfig{}, err
	}

	startCtx, cancel := context.WithTimeout(ctx, time.Duration(srv.StartupTimeout())*time.Second)
	defer cancel()

	handle, err := c.supervisor.StartInstance(
		startCtx,
		process.SharedInstanceID(serverName),
		srv,
		func() error {
			c.coreMu.RLock()
			defer c.coreMu.RUnlock()
			if c.configGeneration == snapshotGeneration {
				return nil
			}
			current, exists := c.cfg.GetServer(serverName)
			if !exists {
				return fmt.Errorf("server removed during config reload: %s", serverName)
			}
			currentNormalized, normalizeErr := normalizedServerConfig(current)
			if normalizeErr != nil {
				return normalizeErr
			}
			if !bytes.Equal(snapshot, currentNormalized) {
				return fmt.Errorf("server config changed during reload: %s", serverName)
			}
			return nil
		},
	)
	if err != nil {
		return nil, config.ServerConfig{}, fmt.Errorf("start server: %w", err)
	}
	if err := handle.WaitForTools(startCtx); err != nil {
		return nil, config.ServerConfig{}, fmt.Errorf("wait for tools: %w", err)
	}
	return handle, srv, nil
}

func normalizedServerConfig(srv config.ServerConfig) ([]byte, error) {
	encoded, err := json.Marshal(srv)
	if err != nil {
		return nil, fmt.Errorf("normalize server config: %w", err)
	}
	return encoded, nil
}

func getOrStartHandle(
	ctx context.Context,
	cfg *config.Config,
	supervisor *process.Supervisor,
	serverName string,
) (*process.Handle, config.ServerConfig, error) {
	srv, ok := cfg.GetServer(serverName)
	if !ok {
		return nil, config.ServerConfig{}, fmt.Errorf("server not found: %s", serverName)
	}
	if !srv.IsEnabled() {
		return nil, config.ServerConfig{}, fmt.Errorf("server is disabled: %s", serverName)
	}

	startCtx, cancel := context.WithTimeout(ctx, time.Duration(srv.StartupTimeout())*time.Second)
	defer cancel()

	handle, err := supervisor.Start(startCtx, serverName, srv)
	if err != nil {
		return nil, config.ServerConfig{}, fmt.Errorf("start server: %w", err)
	}
	if err := handle.WaitForTools(startCtx); err != nil {
		return nil, config.ServerConfig{}, fmt.Errorf("wait for tools: %w", err)
	}
	return handle, srv, nil
}

func (c *Core) getOrStartServer(ctx context.Context, serverName string) (serverClient, *RPCError) {
	handle, srv, err := c.getOrStartHandle(ctx, serverName)
	if err != nil {
		cfg := c.currentConfig()
		if _, ok := cfg.GetServer(serverName); !ok {
			return serverClient{}, ErrServerNotFound(serverName)
		}
		if existing, ok := cfg.GetServer(serverName); ok && !existing.IsEnabled() {
			return serverClient{}, NewRPCError(ErrCodeServerNotRunning, "server is disabled: "+serverName, nil)
		}
		return serverClient{}, ErrServerFailedToStart(serverName, err.Error())
	}
	client := handle.Client()
	if client == nil {
		return serverClient{}, ErrServerNotRunning(serverName)
	}
	return serverClient{
		handle:       handle,
		client:       client,
		timeout:      time.Duration(srv.ToolTimeout()) * time.Second,
		capabilities: handle.Capabilities(),
	}, nil
}

func (c *Core) startWatching(ctx context.Context) {
	if c.configPath == "" {
		return
	}
	c.watchOnce.Do(func() {
		go c.watchConfig(ctx, c.configPath)
	})
}

// Close stops all upstreams and releases Core-owned resources. It is safe to
// call more than once.
func (c *Core) Close() {
	c.closeOnce.Do(func() {
		c.supervisor.StopAll()
		c.bus.Close()
	})
}
