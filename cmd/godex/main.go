package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/app"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/conversation"
	"github.com/tim5wang/godex/internal/core/idempotency"
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/platform/idgen"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/platform/servicecontrol"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
	"github.com/tim5wang/godex/internal/plugins/taskboard"
	rtchannels "github.com/tim5wang/godex/internal/runtime/channels"
	"github.com/tim5wang/godex/internal/runtime/channels/feishu"
	"github.com/tim5wang/godex/internal/runtime/channels/weixin"
	rtcron "github.com/tim5wang/godex/internal/runtime/cron"
	rtheartbeat "github.com/tim5wang/godex/internal/runtime/heartbeat"
	"github.com/tim5wang/godex/internal/runtime/httpapi"
	"github.com/tim5wang/godex/internal/runtime/webui"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/evalharness"
	"github.com/tim5wang/godex/internal/services/nodeobs"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
	"github.com/tim5wang/godex/internal/services/usage"
	"github.com/tim5wang/godex/internal/services/webpush"
	"github.com/tim5wang/godex/internal/tui/mintui"
	"github.com/tim5wang/godex/internal/version"
)

func main() {
	configOptions, args, done, err := prepareRuntimeArgs()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if done {
		return
	}

	manager, err := config.NewManager(configOptions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}
	cfg := manager.Current()

	if err := logger.InitWithConfig(logger.Config{
		Level:      cfg.Logging.Level,
		FilePath:   cfg.Logging.FilePath,
		AlsoStderr: cfg.Logging.AlsoStderr,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize logger: %v\n", err)
		os.Exit(1)
	}
	defer logger.Close()

	// Ensure directories exist
	if err := cfg.EnsureDirs(); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to create directories: %v\n", err)
		os.Exit(1)
	}

	// Allow workspacefs read-only access to godex's own state directories
	// (memory, state, skills, tmp) so tools like read_file can access them
	// even though they sit outside the workspace tree.
	if cfg != nil {
		var dirs []string
		for _, d := range []string{cfg.MemoryDir, cfg.StateDir, cfg.SkillsDir, cfg.TempDir} {
			if d != "" {
				dirs = append(dirs, d)
			}
		}
		workspacefs.DefaultReadAllowlist = dirs
	}

	if cfg.DefaultModelProfile().APIKey == "" && shouldWarnMissingAPIKey(args) {
		fmt.Fprintln(os.Stderr, "Warning: default LLM provider credential is not configured. Chat turns will fail until api.providers has an api_key or api_key_env with a present environment value.")
	}

	usageStore, err := usage.NewSQLiteStore(cfg.StateDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to initialize usage store: %v\n", err)
		os.Exit(1)
	}
	defer usageStore.Close()
	usageService := usage.NewService(usageStore)
	conversation.SetUsageObserver(func(ctx context.Context, event conversation.UsageEvent) {
		_ = ctx
		if err := usageService.RecordLLMUsage(event); err != nil {
			logger.Warnf("record usage event: %v", err)
		}
	})
	defer conversation.SetUsageObserver(nil)

	shared := agent.NewSharedDependencies(cfg)
	commandService := commands.NewService(cfg)
	commandService.SetDoctor(manager.Doctor)
	service := backend.NewService(cfg, shared, commandService)
	// Taskboard plugin (需求池 #1): host-authoritative cross-session task
	// board. Ledger + plugin activation are process-level; executions run
	// as durable subagents from live sessions. The plugin's HTTP surface is
	// mounted into the web mux by httpapi via PluginManager().
	// Reuse the shared ledger instance so the per-session taskboard tool, the
	// executor, and this plugin HTTP surface all serialize through one handle
	// (no double-writer race on ledger.json).
	taskboardLedger := service.TaskboardLedger()
	if taskboardLedger == nil {
		logger.Warnf("taskboard ledger unavailable; taskboard plugin disabled")
	} else if pm := service.PluginManager(); pm != nil {
		executor := backend.NewTaskboardExecutor(service, taskboardLedger)
		// M5 PJM: the per-session taskboard tool (dispatch action) needs the
		// same executor so PJM can start/reuse card execution sessions from
		// its own conversation.
		shared.SetTaskboardExecutor(executor)
		if _, actErr := pm.Activate(context.Background(), taskboard.NewPlugin(taskboardLedger, executor, nil)); actErr != nil {
			logger.Warnf("taskboard plugin activation failed: %v", actErr)
		}
	}
	channelManager := rtchannels.NewManager(cfg, service)
	cronService := rtcron.NewService(rtcron.Config{
		Enabled:           cfg.Cron.Enabled,
		TickSeconds:       cfg.Cron.TickSeconds,
		DefaultTimezone:   cfg.Cron.DefaultTimezone,
		MaxConcurrentRuns: cfg.Cron.MaxConcurrentRuns,
		WorkspaceDir:      cfg.WorkspaceDir,
	}, rtcron.NewFileStore(cfg.StateDir), rtcron.NewBackendAdapter(service), channelManager,
		rtcron.WithIdempotencyStore(idempotency.NewSQLiteStore(cfg.StateDir, 0)))
	cronToolAdapter := rtcron.NewToolAdapter(cronService)
	shared.SetCronService(cronToolAdapter)
	heartbeatService := rtheartbeat.NewService(rtheartbeat.Config{
		Enabled:                cfg.Heartbeat.Enabled,
		TickSeconds:            cfg.Heartbeat.TickSeconds,
		ChecklistPath:          cfg.Heartbeat.ChecklistPath,
		WorkspaceDir:           cfg.WorkspaceDir,
		StateDir:               cfg.StateDir,
		OKToken:                cfg.Heartbeat.OKToken,
		DefaultIntervalSeconds: cfg.Heartbeat.DefaultIntervalSeconds,
		DefaultTimezone:        cfg.Heartbeat.DefaultTimezone,
		DefaultWatchdogScript:  cfg.Heartbeat.DefaultWatchdogScript,
	}, rtheartbeat.NewFileStore(cfg.StateDir), service, channelManager,
		rtheartbeat.WithIdempotencyStore(idempotency.NewSQLiteStore(cfg.StateDir, 0)))
	heartbeatToolAdapter := rtheartbeat.NewToolAdapter(heartbeatService)
	shared.SetHeartbeatService(heartbeatToolAdapter)
	commandService.SetCron(app.NewCronCommandHandler(cronToolAdapter))
	commandService.SetHeartbeat(app.NewHeartbeatCommandHandler(heartbeatToolAdapter))
	commandService.SetModel(app.NewModelCommandHandlerWithSessions(manager, service))
	weixinAuth := weixin.NewWebAuth(manager.Current, func(ctx context.Context, nextCfg *config.Config) error {
		return channelManager.Reconcile(ctx, nextCfg)
	})
	sessionAdmin := sessionadmin.NewService(
		manager.Current,
		service,
		weixinAuthAdapter{auth: weixinAuth},
		func(stateDir, accountID, userID string, reveal bool) (sessionadmin.WeixinContextTokenInspection, error) {
			inspection, err := weixin.InspectContextTokens(stateDir, accountID, userID, reveal)
			if err != nil {
				return sessionadmin.WeixinContextTokenInspection{}, err
			}
			return sessionadmin.WeixinContextTokenInspection{
				AccountID:   inspection.AccountID,
				UserID:      inspection.UserID,
				TokenCount:  inspection.TokenCount,
				UpdatedAt:   inspection.UpdatedAt,
				TokenMasked: inspection.TokenMasked,
				Token:       inspection.Token,
			}, nil
		},
	)
	shared.SetSessionAdminService(sessionAdmin)
	commandService.SetSession(app.NewSessionCommandHandler(service, sessionAdmin))
	commandService.SetClear(app.NewClearCommandHandler(sessionAdmin))
	commandService.SetApprove(app.NewApproveCommandHandler(sessionAdmin))
	commandService.SetDeny(app.NewDenyCommandHandler(sessionAdmin))
	manager.SetDoctorAugmenter(func(report config.DoctorReport) config.DoctorReport {
		report = channelManager.AugmentDoctor(report)
		report = cronService.AugmentDoctor(report)
		report = heartbeatService.AugmentDoctor(report)
		report = augmentPackageAppDoctor(report, manager.Current())
		return report
	})
	for _, factory := range []rtchannels.Factory{
		feishu.Factory{},
		weixin.Factory{},
	} {
		channelManager.RegisterFactory(factory)
		if schemaProvider, ok := factory.(rtchannels.SchemaProvider); ok {
			manager.RegisterSectionSchema(schemaProvider.ConfigSchema())
		}
	}
	commandService.SetChannels(channelManager.StatusText)
	manager.SetApplier(func(ctx context.Context, oldCfg, newCfg *config.Config) config.ApplyReport {
		return applyRuntimeConfig(ctx, oldCfg, newCfg, service, channelManager, cronService, heartbeatService)
	})

	runner := &app.Runner{
		Cfg:                cfg,
		ConfigManager:      manager,
		Backend:            service,
		Stdin:              os.Stdin,
		Stdout:             os.Stdout,
		Stderr:             os.Stderr,
		DefaultSessionSpec: configOptions.SessionSpec,
		RunTUI: func(ctx context.Context, locator backend.SessionLocator) error {
			return mintui.New(cfg, mintui.NewBackendAdapter(service), os.Stdout, os.Stderr).Run(ctx, locator)
		},
		Doctor: func(ctx context.Context) (string, error) {
			_ = ctx
			return manager.Doctor().Text(), nil
		},
		WeixinSetup: func(ctx context.Context) error {
			return weixin.Setup(ctx, cfg, os.Stdout)
		},
		WeixinLogout: func(ctx context.Context) error {
			return weixin.Logout(ctx, cfg, os.Stdout)
		},
		Eval: &evalharness.Service{
			Backend:  service,
			LeadName: cfg.LeadName,
		},
		Serve: func(ctx context.Context, addr string) error {
			controlRegistry, err := noderegistry.New(
				filepath.Join(cfg.HomeDir, "control", "nodes.json"),
				time.Duration(cfg.Control.OfflineAfterSeconds)*time.Second,
			)
			if err != nil {
				return err
			}
			if err := controlRegistry.SeedConfigured(ctx, noderegistry.ConfiguredNodes(cfg.Control.Nodes)); err != nil {
				return err
			}
			selfEndpoint := noderegistry.EndpointForAddr(addr)
			selfNode, err := noderegistry.SelfNodeWithVersion(cfg, selfEndpoint, version.Current().Version)
			if err != nil {
				return err
			}
			if _, err := controlRegistry.Register(ctx, selfNode); err != nil {
				return err
			}
			// The center itself is a node: it is always reachable locally, so mark
			// its relay status as connected (the web UI enables Chat/Terminal/Files
			// for connected nodes and serves them via the local-direct proxy path).
			_ = controlRegistry.SetRelayStatus(ctx, selfNode.ID, "connected")
			// Relay hub: accepts outbound WSS connections from nodes and validates
			// each node's per-node credential against the registry's stored hash.
			relayHub := relay.NewHub(func(nodeID, credential string) bool {
				node, err := controlRegistry.Get(context.Background(), nodeID)
				if err != nil {
					return false
				}
				return relay.ValidateCredential(credential, node.CredentialHash)
			})
			relayHub.SetStatusHook(func(nodeID string, online bool) {
				status := "disconnected"
				if online {
					status = "connected"
				}
				_ = controlRegistry.SetRelayStatus(context.Background(), nodeID, status)
			})
			// Refresh the node's last-seen time on every relay pong so the
			// nodes page shows a live heartbeat for join-onboarded nodes too
			// (their HTTP heartbeat may not authenticate against the center's
			// web token, but a healthy relay connection is authoritative).
			relayHub.SetActivityHook(func(nodeID string) {
				_ = controlRegistry.Touch(context.Background(), nodeID)
			})

			// Observation store: aggregates node-pushed snapshot events so the
			// center can render per-node overviews. The center only keeps the
			// latest snapshot + bounded recent events in memory; session history
			// itself stays on each node.
			eventStore := relay.NewEventStore()
			relayHub.SetEventSink(relay.StoreEvents(eventStore))

			// Distributed browser runtime: when the center's browser tool is
			// configured with cdp_relay_node, drive the Chromium CDP endpoint
			// exposed on that node over the relay channel.
			if strings.TrimSpace(cfg.Tools.Browser.CDPRelayNode) != "" {
				shared.SetBrowserCDPDialer(func(ctx context.Context, nodeID, target string) (net.Conn, error) {
					stream, err := relayHub.OpenTCPStream(ctx, nodeID, idgen.New("cdp-", 4), target)
					if err != nil {
						return nil, err
					}
					return &relayNetConnAdapter{ReadWriteCloser: stream}, nil
				})
			}

			apiHandler := httpapi.NewHandlerWithDependencies(httpapi.Dependencies{
				Config:     manager,
				Backend:    service,
				Channels:   channelManager,
				WeixinAuth: weixinAuth,
				Cron:       cronToolAdapter,
				Heartbeat:  heartbeatToolAdapter,
				ServiceRuntime: serviceRuntimeControl{
					controller: servicecontrol.NewController(),
					options:    serviceRuntimeOptions(manager),
				},
				Usage:           usageService,
				ControlRegistry: &registryWithOverview{Registry: controlRegistry, EventStore: eventStore, Hub: relayHub},
			})

			// Combine the API handler with relay endpoints. The webui strips the
			// leading /api before delegating here, so relay paths are prefix-free:
			//   external /api/relay                       → /relay (WSS upgrade)
			//   external /api/control/nodes/{id}/proxy/.. → /control/nodes/{id}/proxy/..
			root := http.NewServeMux()
			root.Handle("/relay", relayHub)

			// Managed TCP forward tunnels: the center process listens on
			// 127.0.0.1:<local_port> and relays each connection over a node's
			// relay channel to its target (ssh -L style), so an LLM gateway on
			// an internal node can be reached from the center as localhost.
			forwardServer := relay.NewForwardServer(relayHub)
			for _, fwd := range cfg.Control.Forwards {
				if _, err := forwardServer.Add(relay.ForwardSpec{
					ID:        fwd.ID,
					Name:      fwd.Name,
					NodeID:    fwd.NodeID,
					LocalPort: fwd.LocalPort,
					Target:    fwd.Target,
				}); err != nil {
					logger.Errorf("load forward %q: %v", fwd.ID, err)
				}
			}
			registerForwardRoutes(root, forwardServer, manager, relayAuthorize(cfg))
			proxy := relay.NewProxyHandler(relayHub, relayAuthorize(cfg))
			// The center also runs as its own node: requests targeting the self
			// node are served locally (no relay round-trip), so the server can be
			// operated from its own web UI.
			proxy.SetLocalHandler(selfNode.ID, apiHandler)
			// guarded-remote nodes require an explicit approval header on
			// mutating requests; resolve trust level from the registry.
			proxy.TrustLevel = func(nodeID string) string {
				node, err := controlRegistry.Get(context.Background(), nodeID)
				if err != nil {
					return ""
				}
				return node.TrustLevel
			}
			root.Handle("/control/nodes/{id}/proxy/", proxy)
			root.Handle("/control/nodes/{id}/forward", relay.NewForwardHandler(relayHub, relayAuthorize(cfg)))
			// Web Push: in-memory subscriptions, VAPID keys persisted across
			// restarts so browser subscriptions keep working. The center only
			// relays live events — no durable push history.
			pushService, err := webpush.LoadOrCreate(cfg.StateDir)
			if err != nil {
				return err
			}
			root.Handle("/push/", httpapi.NewPushHandler(pushService, relayAuthorize(cfg)))
			root.Handle("/", apiHandler)
			handler, err := webui.NewHandler(root, filepath.Join(cfg.WorkspaceDir, "internal", "uiassets", "embedded_dist"))
			if err != nil {
				return err
			}

			lifecycle := []app.LifecycleService{
				app.NewConfigReloadWatcher(manager),
				noderegistry.NewLocalHeartbeat(controlRegistry, selfNode, time.Duration(cfg.Control.HeartbeatSeconds)*time.Second),
				relayHub,
				forwardServer,
				channelManager,
				cronService,
				heartbeatService,
			}
			if remote := remoteControlHeartbeat(cfg, selfNode, selfEndpoint); remote != nil {
				lifecycle = append(lifecycle, remote)
			}
			if agent := remoteRelayAgent(cfg, selfNode, apiHandler); agent != nil {
				lifecycle = append(lifecycle, agent)
				// Node-side observer: periodically pushes the local observation
				// snapshot (sessions, longtasks, approvals) to the center so the
				// center web can show live progress.
				provider := nodeobs.NewProvider(service, selfNode.Version, selfNode.Capabilities)
				lifecycle = append(lifecycle, relay.NewObserver(agent, provider, 0))
			}
			lifecycle = append(lifecycle, servicecontrol.NewNotifyServiceFromEnv())
			return app.ServeRuntime{
				Server: app.BindHTTPServerContext(ctx, &http.Server{
					Addr:    addr,
					Handler: handler,
				}),
				Services:        lifecycle,
				ShutdownTimeout: 10 * time.Second,
			}.Run(ctx)
		},
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runner.Run(rootCtx, args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// splitDebugArgs separates debug flags from the rest of the argv
// before config parsing. We accept the same prefix set as
// parseDebugFlags. Unknown debug-prefixed arguments are left in the
// remainder so parseDebugFlags can report them with a precise error.
func splitDebugArgs(args []string) (debug []string, remainder []string) {
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--pprof-addr="),
			strings.HasPrefix(arg, "--dump-dir="),
			strings.HasPrefix(arg, "--heap-dump="):
			debug = append(debug, arg)
		default:
			remainder = append(remainder, arg)
		}
	}
	return debug, remainder
}

func extractGlobalConfigArgs(args []string) (config.Options, []string, error) {
	options := config.Options{}
	remaining := make([]string, 0, len(args))
	for idx := 0; idx < len(args); idx++ {
		arg := args[idx]
		switch {
		case arg == "--config":
			if idx+1 >= len(args) {
				return options, nil, fmt.Errorf("missing value for --config")
			}
			options.ConfigPath = args[idx+1]
			idx++
		case strings.HasPrefix(arg, "--config="):
			options.ConfigPath = strings.TrimSpace(strings.TrimPrefix(arg, "--config="))
			if options.ConfigPath == "" {
				return options, nil, fmt.Errorf("missing value for --config")
			}
		case arg == "--session":
			if idx+1 >= len(args) {
				return options, nil, fmt.Errorf("missing value for --session")
			}
			options.SessionSpec = args[idx+1]
			idx++
		case strings.HasPrefix(arg, "--session="):
			options.SessionSpec = strings.TrimSpace(strings.TrimPrefix(arg, "--session="))
			if options.SessionSpec == "" {
				return options, nil, fmt.Errorf("missing value for --session")
			}
		default:
			// Global flags are only recognized before the subcommand.
			// From the first non-global token onward, everything belongs
			// to the subcommand, whose own flag set parses per-command
			// flags such as ask/command/tui --session. Scanning the whole
			// argv here would silently shadow those per-command flags.
			remaining = append(remaining, args[idx:]...)
			return options, remaining, nil
		}
	}
	return options, remaining, nil
}

func applyRuntimeConfig(
	ctx context.Context,
	oldCfg *config.Config,
	newCfg *config.Config,
	service *backend.Service,
	channelManager *rtchannels.Manager,
	cronService *rtcron.Service,
	heartbeatService *rtheartbeat.Service,
) config.ApplyReport {
	report := config.ApplyReport{
		AppliedAt:     time.Now(),
		StorageStatus: config.StorageStatusSaved,
		RuntimeStatus: config.RuntimeStatusApplied,
		Message:       "Configuration applied successfully.",
	}
	if err := newCfg.EnsureDirs(); err != nil {
		report.RuntimeStatus = config.RuntimeStatusFailed
		report.Message = "Configuration saved, but runtime preparation failed."
		report.Errors = append(report.Errors, err.Error())
		return report
	}

	rollback := make([]func() error, 0, 4)
	fail := func(message string, err error) config.ApplyReport {
		report.RuntimeStatus = config.RuntimeStatusFailed
		report.Message = message
		if err != nil {
			report.Errors = append(report.Errors, err.Error())
		}
		for i := len(rollback) - 1; i >= 0; i-- {
			if rollbackErr := rollback[i](); rollbackErr != nil {
				report.Warnings = append(report.Warnings, "rollback: "+rollbackErr.Error())
			}
		}
		return report
	}

	if err := logger.InitWithConfig(loggerConfigFrom(newCfg)); err != nil {
		return fail("Configuration saved, but logger reconfiguration failed.", err)
	}
	rollback = append(rollback, func() error {
		return logger.InitWithConfig(loggerConfigFrom(oldCfg))
	})

	if err := service.ApplyConfig(newCfg); err != nil {
		return fail("Configuration saved, but runtime backend reconfiguration failed.", err)
	}
	rollback = append(rollback, func() error {
		return service.ApplyConfig(oldCfg)
	})

	if err := cronService.Reconcile(ctx, cronConfigFrom(newCfg)); err != nil {
		return fail("Configuration saved, but cron reconciliation failed.", err)
	}
	rollback = append(rollback, func() error {
		return cronService.Reconcile(context.Background(), cronConfigFrom(oldCfg))
	})

	if err := heartbeatService.Reconcile(ctx, heartbeatConfigFrom(newCfg)); err != nil {
		return fail("Configuration saved, but heartbeat reconciliation failed.", err)
	}
	rollback = append(rollback, func() error {
		return heartbeatService.Reconcile(context.Background(), heartbeatConfigFrom(oldCfg))
	})

	if err := channelManager.Reconcile(ctx, newCfg); err != nil {
		return fail("Configuration saved, but channel reconciliation failed.", err)
	}

	return report
}

type weixinAuthAdapter struct {
	auth *weixin.WebAuth
}

type serviceRuntimeControl struct {
	controller *servicecontrol.Controller
	options    servicecontrol.InstallOptions
}

func (c serviceRuntimeControl) Status(ctx context.Context) (any, error) {
	if c.options.Name == "" || c.options.Scope == "" {
		return servicecontrol.Status{
			OS:      runtime.GOOS,
			Managed: false,
			Detail:  "GODEX_SERVICE_NAME/GODEX_SERVICE_SCOPE are not set; this process was not started by `godex service`.",
		}, nil
	}
	return c.controller.Status(ctx, c.options)
}

func (c serviceRuntimeControl) Restart(ctx context.Context) error {
	if c.options.Name == "" || c.options.Scope == "" {
		return fmt.Errorf("this process was not started by `godex service`; restart from Settings is unavailable")
	}
	_, err := c.controller.Restart(ctx, c.options)
	return err
}

func serviceRuntimeOptions(manager *config.Manager) servicecontrol.InstallOptions {
	meta := manager.Meta()
	cfg := manager.Current()
	opts := servicecontrol.CurrentOptions()
	opts.WorkingDir = firstNonEmpty(meta.ProjectDir, cfg.ProjectDir, cfg.WorkspaceDir)
	opts.HomeDir = firstNonEmpty(meta.HomeDir, cfg.HomeDir)
	opts.ProjectDir = firstNonEmpty(meta.ProjectDir, cfg.ProjectDir, cfg.WorkspaceDir)
	opts.LogPath = cfg.Logging.FilePath
	return opts
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// registryWithOverview combines the node registry with the relay observation
// store so the httpapi handler can serve both node CRUD endpoints and the
// aggregated per-node overview from a single argument. It also carries the
// relay hub so deleting a node can drop its live relay connection.
type registryWithOverview struct {
	*noderegistry.Registry
	*relay.EventStore
	Hub *relay.Hub
}

// DisconnectNode forcibly closes the node's relay connection (httpapi delete
// endpoint calls this after removing the node from the registry).
func (r *registryWithOverview) DisconnectNode(nodeID string) {
	if r.Hub != nil {
		r.Hub.Disconnect(nodeID)
	}
}

func remoteControlHeartbeat(cfg *config.Config, selfNode noderegistry.NodeInput, selfEndpoint string) app.LifecycleService {
	centerURL := strings.TrimRight(strings.TrimSpace(cfg.Control.CenterURL), "/")
	if centerURL == "" {
		return nil
	}
	if selfEndpoint != "" && strings.EqualFold(centerURL, strings.TrimRight(selfEndpoint, "/")) {
		return nil
	}
	interval := time.Duration(cfg.Control.HeartbeatSeconds) * time.Second
	return noderegistry.NewRemoteHeartbeat(centerURL, cfg.WebToken, selfNode, interval)
}

// remoteRelayAgent starts the node-side relay agent when this instance is
// configured to join a center (control.center_url + control.credential) that is
// not itself. The agent dials the center outbound and serves forwarded requests
// against this node's local httpapi handler.
func remoteRelayAgent(cfg *config.Config, selfNode noderegistry.NodeInput, localHandler http.Handler) *relay.Agent {
	centerURL := strings.TrimRight(strings.TrimSpace(cfg.Control.CenterURL), "/")
	if centerURL == "" {
		return nil
	}
	if selfNode.Endpoint != "" && strings.EqualFold(centerURL, strings.TrimRight(selfNode.Endpoint, "/")) {
		return nil
	}
	credential := strings.TrimSpace(cfg.Control.Credential)
	if credential == "" {
		return nil
	}
	return relay.NewAgent(relay.AgentConfig{
		CenterURL:    centerURL,
		NodeID:       selfNode.ID,
		Credential:   credential,
		Version:      selfNode.Version,
		Caps:         selfNode.Capabilities,
		Handler:      localHandler,
		ForwardAllow: cfg.Control.ForwardAllow,
	})
}

// relayAuthorize protects the center-side proxy endpoint with the same web
// token policy as the rest of the API: when no web token is configured all
// requests pass; otherwise the request must carry a matching Bearer token.
func relayAuthorize(cfg *config.Config) func(*http.Request) bool {
	return func(r *http.Request) bool {
		token := strings.TrimSpace(cfg.WebToken)
		if token == "" {
			return true
		}
		header := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
			return false
		}
		return strings.TrimSpace(header[len("Bearer "):]) == token
	}
}

// registerForwardRoutes wires the managed-forward REST surface onto the root
// mux (external paths carry the /api prefix the webui strips):
//
//	GET    /control/forwards           list tunnels (with runtime status)
//	POST   /control/forwards           create a tunnel (persisted to config)
//	DELETE /control/forwards/{id}      remove a tunnel (persisted)
//	POST   /control/forwards/{id}/check  probe the tunnel end to end
func registerForwardRoutes(mux *http.ServeMux, server *relay.ForwardServer, manager *config.Manager, authorize func(*http.Request) bool) {
	guard := func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authorize != nil && !authorize(r) {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			next(w, r)
		})
	}

	mux.Handle("GET /control/forwards", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeForwardJSON(w, http.StatusOK, server.List())
	})))

	mux.Handle("POST /control/forwards", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Name      string `json:"name"`
			NodeID    string `json:"node_id"`
			LocalPort int    `json:"local_port"`
			Target    string `json:"target"`
		}
		if err := decodeForwardBody(r, &req); err != nil {
			http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
			return
		}
		spec, err := server.Add(relay.ForwardSpec{
			Name:      strings.TrimSpace(req.Name),
			NodeID:    strings.TrimSpace(req.NodeID),
			LocalPort: req.LocalPort,
			Target:    strings.TrimSpace(req.Target),
		})
		if err != nil {
			http.Error(w, `{"error":"`+jsonEscape(err.Error())+`"}`, http.StatusBadRequest)
			return
		}
		if err := persistForwards(r.Context(), manager, server); err != nil {
			http.Error(w, `{"error":"persist failed"}`, http.StatusInternalServerError)
			return
		}
		writeForwardJSON(w, http.StatusCreated, spec)
	})))

	mux.Handle("DELETE /control/forwards/{id}", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !server.Remove(id) {
			http.Error(w, `{"error":"forward not found"}`, http.StatusNotFound)
			return
		}
		if err := persistForwards(r.Context(), manager, server); err != nil {
			http.Error(w, `{"error":"persist failed"}`, http.StatusInternalServerError)
			return
		}
		writeForwardJSON(w, http.StatusOK, map[string]bool{"removed": true})
	})))

	mux.Handle("POST /control/forwards/{id}/check", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		result, err := server.Check(r.PathValue("id"))
		if err != nil {
			http.Error(w, `{"error":"forward not found"}`, http.StatusNotFound)
			return
		}
		writeForwardJSON(w, http.StatusOK, result)
	})))
}

// persistForwards writes the currently running tunnel set back into the
// config (control.forwards) so tunnels survive a center restart. The runtime
// server remains the source of truth for state; the config is only the
// persistence layer.
func persistForwards(ctx context.Context, manager *config.Manager, server *relay.ForwardServer) error {
	statuses := server.List()
	sections := make([]config.ForwardSection, 0, len(statuses))
	for _, st := range statuses {
		sections = append(sections, config.ForwardSection{
			ID:        st.ID,
			Name:      st.Name,
			NodeID:    st.NodeID,
			LocalPort: st.LocalPort,
			Target:    st.Target,
		})
	}
	_, err := manager.Update(ctx, config.UpdateRequest{Values: map[string]any{
		"control.forwards": sections,
	}})
	return err
}

func decodeForwardBody(r *http.Request, into any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, into)
}

func writeForwardJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func jsonEscape(s string) string {
	data, err := json.Marshal(s)
	if err != nil {
		return s
	}
	return strings.Trim(string(data), `"`)
}

func augmentPackageAppDoctor(report config.DoctorReport, cfg *config.Config) config.DoctorReport {
	if cfg == nil {
		return report
	}
	items, err := pkgregistry.NewManager(cfg.StateDir, cfg.SkillsDir).List()
	if err != nil {
		report.Checks = append(report.Checks, config.DoctorCheck{
			Severity:   "warning",
			Code:       "package_app_diagnostics_unavailable",
			Path:       "packages",
			Message:    "Failed to inspect package app declarations.",
			Suggestion: err.Error(),
		})
		report.Warnings++
		return report
	}
	for _, item := range items {
		if pkgregistry.AppManifestEmpty(item.App) {
			continue
		}
		for _, issue := range pkgregistry.AppManifestIssues(item.App) {
			report.Checks = append(report.Checks, config.DoctorCheck{
				Severity:   "warning",
				Code:       "package_app_manifest",
				Path:       "packages." + item.Name + ".app",
				Message:    issue,
				Suggestion: "Use app.kind=builtin with a supported builtin app id such as notes, or remove the app declaration.",
			})
			report.Warnings++
		}
	}
	return report
}

func shouldWarnMissingAPIKey(args []string) bool {
	if len(args) == 0 {
		return true
	}
	switch args[0] {
	case "doctor", "help", "-h", "--help", "login", "logout", "migrate", "node", "providers", "repair", "service", "tui", "version", "--version":
		return false
	default:
		return true
	}
}

func (a weixinAuthAdapter) Status(ctx context.Context, accountID string) (sessionadmin.WeixinAuthStatus, error) {
	status, err := a.auth.Status(ctx, accountID)
	if err != nil {
		return sessionadmin.WeixinAuthStatus{}, err
	}
	return convertWeixinAuthStatus(status), nil
}

func (a weixinAuthAdapter) Start(ctx context.Context, accountID string) (sessionadmin.WeixinAuthStatus, error) {
	status, err := a.auth.Start(ctx, accountID)
	if err != nil {
		return sessionadmin.WeixinAuthStatus{}, err
	}
	return convertWeixinAuthStatus(status), nil
}

func (a weixinAuthAdapter) Logout(ctx context.Context, accountID string) (sessionadmin.WeixinAuthStatus, error) {
	status, err := a.auth.Logout(ctx, accountID)
	if err != nil {
		return sessionadmin.WeixinAuthStatus{}, err
	}
	return convertWeixinAuthStatus(status), nil
}

func convertWeixinAuthStatus(status weixin.WebAuthStatus) sessionadmin.WeixinAuthStatus {
	view := sessionadmin.WeixinAuthStatus{
		AccountID:  status.AccountID,
		Enabled:    status.Enabled,
		Configured: status.Configured,
	}
	if status.Login != nil {
		view.Login = &sessionadmin.WeixinAuthLoginStatus{
			Active:       status.Login.Active,
			State:        status.Login.State,
			Message:      status.Login.Message,
			QRCode:       status.Login.QRCode,
			QRCodeImgURL: status.Login.QRCodeImgURL,
		}
	}
	return view
}

func loggerConfigFrom(cfg *config.Config) logger.Config {
	return logger.Config{
		Level:      cfg.Logging.Level,
		FilePath:   cfg.Logging.FilePath,
		AlsoStderr: cfg.Logging.AlsoStderr,
	}
}

func cronConfigFrom(cfg *config.Config) rtcron.Config {
	return rtcron.Config{
		Enabled:           cfg.Cron.Enabled,
		TickSeconds:       cfg.Cron.TickSeconds,
		DefaultTimezone:   cfg.Cron.DefaultTimezone,
		MaxConcurrentRuns: cfg.Cron.MaxConcurrentRuns,
		WorkspaceDir:      cfg.WorkspaceDir,
	}
}

func heartbeatConfigFrom(cfg *config.Config) rtheartbeat.Config {
	return rtheartbeat.Config{
		Enabled:                cfg.Heartbeat.Enabled,
		TickSeconds:            cfg.Heartbeat.TickSeconds,
		ChecklistPath:          cfg.Heartbeat.ChecklistPath,
		WorkspaceDir:           cfg.WorkspaceDir,
		StateDir:               cfg.StateDir,
		OKToken:                cfg.Heartbeat.OKToken,
		DefaultIntervalSeconds: cfg.Heartbeat.DefaultIntervalSeconds,
		DefaultTimezone:        cfg.Heartbeat.DefaultTimezone,
		DefaultWatchdogScript:  cfg.Heartbeat.DefaultWatchdogScript,
	}
}

// relayNetConnAdapter adapts a relay TCP stream (io.ReadWriteCloser) into a
// net.Conn so the CDP WebSocket dialer can run over the relay channel.
// Deadlines are no-ops: the relay channel is governed by the hub's own
// liveness handling.
type relayNetConnAdapter struct {
	io.ReadWriteCloser
}

func (c *relayNetConnAdapter) LocalAddr() net.Addr              { return c }
func (c *relayNetConnAdapter) RemoteAddr() net.Addr             { return c }
func (c *relayNetConnAdapter) Network() string                  { return "relay" }
func (c *relayNetConnAdapter) String() string                   { return "relay-cdp" }
func (c *relayNetConnAdapter) SetDeadline(time.Time) error      { return nil }
func (c *relayNetConnAdapter) SetReadDeadline(time.Time) error  { return nil }
func (c *relayNetConnAdapter) SetWriteDeadline(time.Time) error { return nil }
