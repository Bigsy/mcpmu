package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Bigsy/mcpmu/internal/config"
	"github.com/Bigsy/mcpmu/internal/events"
	"github.com/Bigsy/mcpmu/internal/mcp"
	"github.com/Bigsy/mcpmu/internal/metrics"
	"github.com/Bigsy/mcpmu/internal/process"
)

// Core owns state that can be shared by multiple MCP sessions: upstream
// processes, tool aggregation, configuration, hot-reload input, and the
// upstream notification entry point.
type Core struct {
	coreMu           sync.RWMutex
	cfg              *config.Config
	configGeneration uint64
	recorder         *metrics.Recorder // nil when metrics are disabled or unavailable
	// resourceStateMu serializes the two writers that reset all subscription
	// and resource-map state (Close and hot reload); it is never taken
	// read-side. resources/* handlers and session teardown perform upstream
	// I/O that must not block — or be blocked by — those writers; overlap is
	// handled by the subscription table's epoch check plus per-session locks,
	// not here.
	resourceStateMu sync.Mutex
	sessionsMu      sync.RWMutex
	sessions        map[*Session]struct{}
	nextSessionID   atomic.Uint64
	subscriptions   *resourceSubscriptions

	bus        *events.Bus
	supervisor *process.Supervisor
	aggregator *Aggregator

	notifications NotificationBroadcaster
	reloadCh      chan *config.Config
	configPath    string
	debounceDelay time.Duration
	watchOnce     sync.Once
	reloadOnce    sync.Once
	sharedReload  atomic.Bool
	closeOnce     sync.Once

	// lifetime is cancelled by Close; every Core.spawn goroutine and every
	// Core-scoped upstream call runs under it. bgWG tracks those goroutines
	// so Close returns only once they have stopped.
	lifetime       context.Context
	cancelLifetime context.CancelFunc
	bgWG           sync.WaitGroup
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

	lifetime, cancelLifetime := context.WithCancel(context.Background())
	c := &Core{
		lifetime:         lifetime,
		cancelLifetime:   cancelLifetime,
		cfg:              opts.Config,
		configGeneration: 1,
		recorder:         newRecorderForConfig(opts.Config, opts.ConfigPath),
		sessions:         make(map[*Session]struct{}),
		subscriptions:    newResourceSubscriptions(),
		bus:              bus,
		supervisor:       supervisor,
		reloadCh:         make(chan *config.Config, 1),
		configPath:       opts.ConfigPath,
		debounceDelay:    opts.DebounceDelay,
	}
	c.aggregator = NewAggregator(c.cfg, supervisor, false)
	c.aggregator.acquire = c.getOrStartHandle
	c.notifications = newNotificationBroadcaster(c.processNotification)

	// Supervisor publishes immutable generation-tagged results to Core. It
	// never knows about individual downstream sessions.
	supervisor.SetObserver(c)
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

// currentConfigGeneration snapshots the config generation for stale-install
// detection: state derived from the current config (the resourceMap routing
// table) must not be installed if a reload replaced that config meanwhile.
func (c *Core) currentConfigGeneration() uint64 {
	c.coreMu.RLock()
	defer c.coreMu.RUnlock()
	return c.configGeneration
}

func (c *Core) currentRecorder() *metrics.Recorder {
	c.coreMu.RLock()
	defer c.coreMu.RUnlock()
	return c.recorder
}

// newRecorderForConfig builds the usage-metrics recorder for a config, or nil
// when metrics are disabled, no config path is set, or the sidecar path cannot
// be resolved. A nil recorder is valid everywhere (all methods are no-ops).
func newRecorderForConfig(cfg *config.Config, configPath string) *metrics.Recorder {
	if configPath == "" || !cfg.MetricsEnabled() {
		return nil
	}
	path, err := metrics.MetricsPath(configPath)
	if err != nil {
		log.Printf("Warning: failed to resolve metrics path: %v", err)
		return nil
	}
	return metrics.NewRecorder(path, cfg.MetricsRetentionDays())
}

func (c *Core) replaceConfig(cfg *config.Config) *Aggregator {
	aggregator := NewAggregator(cfg, c.supervisor, false)
	aggregator.acquire = c.getOrStartHandle
	c.coreMu.Lock()
	c.cfg = cfg
	c.aggregator = aggregator
	c.configGeneration++
	// Swap the metrics recorder if metrics.enabled flipped. Retention changes
	// just update the live recorder and take effect on its next flush.
	var stale *metrics.Recorder
	switch {
	case c.recorder == nil:
		c.recorder = newRecorderForConfig(cfg, c.configPath)
	case !cfg.MetricsEnabled():
		stale = c.recorder
		c.recorder = nil
	default:
		c.recorder.SetRetentionDays(cfg.MetricsRetentionDays())
	}
	c.coreMu.Unlock()
	if stale != nil {
		if err := stale.Close(); err != nil {
			log.Printf("Warning: failed to close metrics recorder: %v", err)
		}
	}
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

type serverConfigSnapshot struct {
	generation uint64
	config     config.ServerConfig
	normalized []byte
}

// getOrStartHandle is the one lazy-start/readiness path used by tools,
// resources, prompts, and discovery. It snapshots the complete server config,
// then revalidates it under the instance lifecycle lock before a start.
func (c *Core) getOrStartHandle(ctx context.Context, serverName string) (*process.Handle, config.ServerConfig, error) {
	return c.getOrStartInstance(ctx, process.SharedInstanceID(serverName), serverName)
}

func (c *Core) getOrStartInstance(ctx context.Context, id process.InstanceID, serverName string) (*process.Handle, config.ServerConfig, error) {
	snapshot, err := c.snapshotServerConfig(id, serverName)
	if err != nil {
		return nil, config.ServerConfig{}, err
	}

	startCtx, cancel := context.WithTimeout(ctx, time.Duration(snapshot.config.StartupTimeout())*time.Second)
	defer cancel()

	handle, err := c.supervisor.StartInstance(
		startCtx,
		id,
		snapshot.config,
		func() error { return c.validateServerConfigSnapshot(id, serverName, snapshot) },
	)
	if err != nil {
		return nil, config.ServerConfig{}, fmt.Errorf("start server: %w", err)
	}
	if err := handle.WaitForTools(startCtx); err != nil {
		return nil, config.ServerConfig{}, fmt.Errorf("wait for tools: %w", err)
	}
	return handle, snapshot.config, nil
}

func (c *Core) restartInstance(ctx context.Context, id process.InstanceID, serverName string) (*process.Handle, error) {
	snapshot, err := c.snapshotServerConfig(id, serverName)
	if err != nil {
		return nil, err
	}
	restartCtx, cancel := context.WithTimeout(ctx, time.Duration(snapshot.config.StartupTimeout())*time.Second)
	defer cancel()
	return c.supervisor.RestartInstanceValidated(
		restartCtx,
		id,
		snapshot.config,
		func() error { return c.validateServerConfigSnapshot(id, serverName, snapshot) },
	)
}

func (c *Core) snapshotServerConfig(id process.InstanceID, serverName string) (serverConfigSnapshot, error) {
	c.coreMu.RLock()
	snapshotGeneration := c.configGeneration
	srv, ok := c.cfg.GetServer(serverName)
	c.coreMu.RUnlock()
	if !ok {
		return serverConfigSnapshot{}, fmt.Errorf("server not found: %s", serverName)
	}
	if !srv.IsEnabled() {
		return serverConfigSnapshot{}, fmt.Errorf("server is disabled: %s", serverName)
	}
	if srv.IsShared() != id.IsShared() {
		return serverConfigSnapshot{}, fmt.Errorf("server sharing mode changed: %s", serverName)
	}
	normalized, err := normalizedServerConfig(srv)
	if err != nil {
		return serverConfigSnapshot{}, err
	}
	return serverConfigSnapshot{generation: snapshotGeneration, config: srv, normalized: normalized}, nil
}

func (c *Core) validateServerConfigSnapshot(id process.InstanceID, serverName string, snapshot serverConfigSnapshot) error {
	c.coreMu.RLock()
	defer c.coreMu.RUnlock()
	if c.configGeneration == snapshot.generation {
		return nil
	}
	current, exists := c.cfg.GetServer(serverName)
	if !exists {
		return fmt.Errorf("server removed during config reload: %s", serverName)
	}
	if current.IsShared() != id.IsShared() {
		return fmt.Errorf("server sharing mode changed during reload: %s", serverName)
	}
	currentNormalized, err := normalizedServerConfig(current)
	if err != nil {
		return err
	}
	if !bytes.Equal(snapshot.normalized, currentNormalized) {
		return fmt.Errorf("server config changed during reload: %s", serverName)
	}
	return nil
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

func (c *Core) startWatching(ctx context.Context) {
	if c.configPath == "" {
		return
	}
	c.watchOnce.Do(func() {
		c.spawn("config watcher", func(lifetime context.Context) error {
			watchCtx, cancel := joinContext(ctx, lifetime)
			defer cancel()
			c.watchConfig(watchCtx, c.configPath)
			return nil
		})
	})
}

// StartWatching binds the Core-owned config watcher to the caller's
// lifecycle. Daemon mode uses a daemon-wide context so the watcher is not
// accidentally owned by whichever client session connects first.
func (c *Core) StartWatching(ctx context.Context) {
	c.sharedReload.Store(true)
	c.startWatching(ctx)
	c.reloadOnce.Do(func() {
		c.spawn("reload consumer", func(lifetime context.Context) error {
			reloadCtx, cancel := joinContext(ctx, lifetime)
			defer cancel()
			for {
				select {
				case <-reloadCtx.Done():
					return nil
				case cfg := <-c.reloadCh:
					c.applyReload(reloadCtx, cfg, nil)
				}
			}
		})
	})
}

// RunningServers returns a stable snapshot for daemon status reporting.
func (c *Core) RunningServers() []string {
	return c.supervisor.RunningServers()
}

// HasNamespace reports whether the current config defines the named
// namespace. HTTP serve uses it to answer 404 for a namespace route that
// does not exist, before any Session is created.
func (c *Core) HasNamespace(name string) bool {
	_, ok := c.currentConfig().Namespaces[name]
	return ok
}

func (c *Core) registerSession(session *Session) {
	c.sessionsMu.Lock()
	c.sessions[session] = struct{}{}
	c.sessionsMu.Unlock()
}

func (c *Core) newSessionID() string {
	return fmt.Sprintf("session-%d", c.nextSessionID.Add(1))
}

func (c *Core) sessionForID(id string) *Session {
	c.sessionsMu.RLock()
	defer c.sessionsMu.RUnlock()
	for session := range c.sessions {
		if session.id == id {
			return session
		}
	}
	return nil
}

func (c *Core) aggregatorForInstance(id process.InstanceID) *Aggregator {
	if id.IsShared() {
		return c.currentAggregator()
	}
	if session := c.sessionForID(id.Session); session != nil {
		return session.privateAggregatorSnapshot()
	}
	return nil
}

func (c *Core) unregisterSession(session *Session) {
	c.sessionsMu.Lock()
	delete(c.sessions, session)
	c.sessionsMu.Unlock()
}

func (c *Core) sessionSnapshot() []*Session {
	c.sessionsMu.RLock()
	defer c.sessionsMu.RUnlock()
	sessions := make([]*Session, 0, len(c.sessions))
	for session := range c.sessions {
		sessions = append(sessions, session)
	}
	return sessions
}

// Close stops all upstreams and releases Core-owned resources. It is safe to
// call more than once.
func (c *Core) Close() {
	c.closeOnce.Do(func() {
		// Cancel first so Core-scoped goroutines and upstream calls stop
		// promptly; they are waited for at the end.
		c.cancelLifetime()
		// Short critical section, writer-vs-writer only: in-flight resource
		// handlers and session teardowns are not excluded (they hold no read
		// lock) — subscriptions.clear()'s epoch bump discards their results.
		c.resourceStateMu.Lock()
		c.subscriptions.clear()
		for _, session := range c.sessionSnapshot() {
			session.clearResourceState()
		}
		c.resourceStateMu.Unlock()
		c.supervisor.StopAll()
		c.notifications.Close()
		if err := c.currentRecorder().Close(); err != nil {
			log.Printf("Warning: final metrics flush failed: %v", err)
		}
		c.bus.Close()
		c.bgWG.Wait()
	})
}

// OnDiscoveryResult consumes the Supervisor-owned initial discovery result.
func (c *Core) OnDiscoveryResult(result process.DiscoveryResult) {
	aggregator := c.aggregatorForInstance(result.Instance)
	if aggregator == nil {
		return
	}
	changed, hadPrior := aggregator.applyDiscovery(result)
	if changed && hadPrior {
		c.notifications.Publish(process.UpstreamNotification{
			Instance: result.Instance, Generation: result.Generation,
			Method: "notifications/tools/list_changed",
		})
	}
	if result.Initialized {
		c.spawn("replay subscriptions", func(ctx context.Context) error {
			c.replaySubscriptions(ctx, result.Instance, result.Generation)
			return nil
		})
	}
}

// OnInstanceStopped invalidates verification while retaining the last-good
// tool set for partial responses and diagnostics.
func (c *Core) OnInstanceStopped(id process.InstanceID, generation uint64) {
	if aggregator := c.aggregatorForInstance(id); aggregator != nil {
		aggregator.invalidateInstance(id, generation)
	}
}

// OnUpstreamNotification is called on the MCP response-reader goroutine and
// therefore only enqueues work for the broadcaster worker.
func (c *Core) OnUpstreamNotification(notification process.UpstreamNotification) {
	c.notifications.OnUpstreamNotification(notification)
}

func (c *Core) processNotification(notification process.UpstreamNotification) bool {
	handle := c.supervisor.GetInstance(notification.Instance)
	if notification.Upstream && (handle == nil || !handle.IsRunning() || handle.Generation() != notification.Generation) {
		return false
	}
	if notification.Upstream && notification.Method == "notifications/tools/list_changed" {
		ctx, cancel := context.WithTimeout(c.lifetime, DefaultToolDiscoveryTimeout)
		aggregator := c.aggregatorForInstance(notification.Instance)
		if aggregator == nil {
			cancel()
			return false
		}
		err := aggregator.refreshHandleTools(ctx, handle)
		cancel()
		if err != nil {
			log.Printf("Failed to refresh tools after upstream list_changed from %s: %v", notification.Instance, err)
		}
	}
	if notification.Method == "notifications/resources/updated" {
		var params struct {
			URI string `json:"uri"`
		}
		if err := json.Unmarshal(notification.Params, &params); err != nil || params.URI == "" {
			return false
		}
		return c.subscriptions.hasSubscribers(resourceSubscriptionKey{
			Instance: notification.Instance,
			URI:      params.URI,
		})
	}
	return true
}

func (c *Core) subscribeResource(
	ctx context.Context,
	session *Session,
	key resourceSubscriptionKey,
	server serverClient,
) error {
	dropped, err := c.subscriptions.subscribe(session, key, server.handle.Generation(), func() error {
		callCtx, cancel := context.WithTimeout(ctx, server.timeout)
		defer cancel()
		return server.client.SubscribeResource(callCtx, key.URI)
	})
	c.notifyDroppedSubscriptions(dropped)
	return err
}

func (c *Core) unsubscribeResource(ctx context.Context, session *Session, key resourceSubscriptionKey) {
	err := c.subscriptions.unsubscribe(session, key, func(generation uint64) error {
		return c.unsubscribeUpstream(ctx, key, generation)
	})
	if err != nil {
		log.Printf("resources/unsubscribe on %s for %q failed after local cleanup: %v", key.Instance, key.URI, err)
	}
}

func (c *Core) unsubscribeUpstream(ctx context.Context, key resourceSubscriptionKey, generation uint64) error {
	handle := c.supervisor.GetInstance(key.Instance)
	if handle == nil || !handle.IsRunning() || handle.Generation() != generation || handle.Client() == nil {
		return nil
	}
	srv, ok := c.currentConfig().GetServer(key.Instance.Server)
	if !ok {
		return nil
	}
	callCtx, cancel := context.WithTimeout(ctx, time.Duration(srv.ToolTimeout())*time.Second)
	defer cancel()
	return handle.Client().UnsubscribeResource(callCtx, key.URI)
}

func (c *Core) cleanupSessionSubscriptions(session *Session) {
	for uri, instance := range session.subscriptionSnapshot() {
		key := resourceSubscriptionKey{Instance: instance, URI: uri}
		c.unsubscribeResource(context.Background(), session, key)
	}
}

func (c *Core) replaySubscriptions(ctx context.Context, id process.InstanceID, generation uint64) {
	for _, key := range c.subscriptions.keysForInstance(id) {
		handle := c.supervisor.GetInstance(id)
		if handle == nil || !handle.IsRunning() || handle.Generation() != generation || handle.Client() == nil {
			return
		}
		capabilities := handle.Capabilities()
		dropped, err := c.subscriptions.replay(key, generation, func() error {
			if capabilities.Resources == nil || !capabilities.Resources.Subscribe {
				return fmt.Errorf("upstream %s no longer supports resources/subscribe", id)
			}
			srv, ok := c.currentConfig().GetServer(id.Server)
			if !ok {
				return fmt.Errorf("server no longer exists: %s", id.Server)
			}
			callCtx, cancel := context.WithTimeout(ctx, time.Duration(srv.ToolTimeout())*time.Second)
			defer cancel()
			return handle.Client().SubscribeResource(callCtx, key.URI)
		})
		if err != nil {
			log.Printf("Failed to replay resources/subscribe on %s for %q: %v", id, key.URI, err)
		}
		c.notifyDroppedSubscriptions(dropped)
	}
}

func (c *Core) notifyDroppedSubscriptions(sessions []*Session) {
	for _, session := range sessions {
		if session.opts.ExposeResources {
			session.sendNotification("notifications/resources/list_changed")
		}
	}
}

func (c *Core) clearResourceStateForReload() {
	c.subscriptions.clear()
	for _, session := range c.sessionSnapshot() {
		session.clearResourceState()
	}
}

var _ process.Observer = (*Core)(nil)
