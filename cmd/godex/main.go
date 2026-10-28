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
	"github.com/tim5wang/godex/internal/core/identity"
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
	"github.com/tim5wang/godex/internal/services/accounts"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/evalharness"
	"github.com/tim5wang/godex/internal/services/metering"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
	"github.com/tim5wang/godex/internal/services/usage"
	"github.com/tim5wang/godex/internal/services/webpush"
	"github.com/tim5wang/godex/internal/tui/mintui"
	"github.com/tim5wang/godex/internal/version"
)

// configureResolverFromEnv installs a custom net.Resolver when
// GODEX_DNS_SERVERS (comma-separated DNS server addresses) is set.
// Android app sandboxes ship an unusable /etc/resolv.conf ([::1]:53 with no
// listener), so the pure-Go resolver needs explicit server addresses.
func configureResolverFromEnv() {
	servers := os.Getenv("GODEX_DNS_SERVERS")
	if strings.TrimSpace(servers) == "" {
		return
	}
	var addrs []string
	for _, s := range strings.Split(servers, ",") {
		if s = strings.TrimSpace(s); s != "" {
			addrs = append(addrs, s)
		}
	}
	if len(addrs) == 0 {
		return
	}
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{Timeout: 5 * time.Second}
			var lastErr error
			for _, a := range addrs {
				conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(a, "53"))
				if err == nil {
					return conn, nil
				}
				lastErr = err
			}
			return nil, lastErr
		},
	}
}

func main() {
	configureResolverFromEnv()
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
	// Multi-tenant accounts (design §3.1). Extracted to a helper so main's branch
	// count stays within the complexity budget, and so the cleanup is returned
	// rather than deferred inside the helper (a defer there would close the store
	// the moment the helper returned).
	accountsService, closeAccounts := openAccountsStore(cfg)
	defer closeAccounts()
	// Relay metering (design §7). Opened before the proxy is built because the
	// proxy records into it; a failure degrades to "no metering" rather than
	// refusing to start, since the forwarding itself must keep working.
	meteringService, closeMetering := openMeteringStore(cfg)
	defer closeMetering()
	conversation.SetUsageObserver(func(ctx context.Context, event conversation.UsageEvent) {
		_ = ctx
		if err := usageService.RecordLLMUsage(event); err != nil {
			logger.Warnf("record usage event: %v", err)
		}
	})
	defer conversation.SetUsageObserver(nil)

	shared := agent.NewSharedDependencies(cfg)
	defer func() {
		if manager := shared.MCPManager(); manager != nil {
			manager.Close()
		}
	}()
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
	var taskboardExecutor *backend.TaskboardExecutor
	if taskboardLedger == nil {
		logger.Warnf("taskboard ledger unavailable; taskboard plugin disabled")
	} else if pm := service.PluginManager(); pm != nil {
		executor := backend.NewTaskboardExecutor(service, taskboardLedger)
		taskboardExecutor = executor
		// M5 PJM: the per-session taskboard tool (dispatch action) needs the
		// same executor so PJM can start/reuse card execution sessions from
		// its own conversation.
		shared.SetTaskboardExecutor(executor)
		if _, actErr := pm.Activate(context.Background(), taskboard.NewPlugin(taskboardLedger, executor, nil)); actErr != nil {
			logger.Warnf("taskboard plugin activation failed: %v", actErr)
		}
	}
	channelManager := rtchannels.NewManager(cfg, service)
	// Wire the taskboard-backed watchdog-directive evaluator so cron jobs can
	// gate on declarative taskboard status counts instead of a python watchdog
	// script (需求池: cron 调用 godex 内联指令).
	var cronService *rtcron.Service
	if taskboardLedger != nil {
		cronDirectiveEvaluator := backend.NewTaskboardDirectiveEvaluator(taskboardExecutor, taskboardLedger)
		cronService = rtcron.NewService(rtcron.Config{
			Enabled:           cfg.Cron.Enabled,
			TickSeconds:       cfg.Cron.TickSeconds,
			DefaultTimezone:   cfg.Cron.DefaultTimezone,
			MaxConcurrentRuns: cfg.Cron.MaxConcurrentRuns,
			WorkspaceDir:      cfg.WorkspaceDir,
		}, rtcron.NewFileStore(cfg.StateDir), rtcron.NewBackendAdapter(service), channelManager,
			rtcron.WithIdempotencyStore(idempotency.NewSQLiteStore(cfg.StateDir, 0)),
			rtcron.WithDirectiveEvaluator(cronDirectiveEvaluator))
	} else {
		cronService = rtcron.NewService(rtcron.Config{
			Enabled:           cfg.Cron.Enabled,
			TickSeconds:       cfg.Cron.TickSeconds,
			DefaultTimezone:   cfg.Cron.DefaultTimezone,
			MaxConcurrentRuns: cfg.Cron.MaxConcurrentRuns,
			WorkspaceDir:      cfg.WorkspaceDir,
		}, rtcron.NewFileStore(cfg.StateDir), rtcron.NewBackendAdapter(service), channelManager,
			rtcron.WithIdempotencyStore(idempotency.NewSQLiteStore(cfg.StateDir, 0)))
	}
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
	// controlRT is captured by the config applier below so control edits
	// (join / leave / center switch / forward_allow) hot-reload into the
	// running node-side relay components without restarting serve. It is
	// assigned inside Serve once the serve context and bridge exist.
	var controlRT *controlRuntime
	manager.SetApplier(func(ctx context.Context, oldCfg, newCfg *config.Config) config.ApplyReport {
		if controlRT != nil {
			controlRT.Reconcile(oldCfg, newCfg)
		}
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
			// One NodeAccess policy for every seam in this process (registry reads in
			// the API handler, the relay proxy below, and the TCP forward server that
			// reuses proxy.AuthorizeNode), carrying the superadmin's tenant sharing
			// switch. Defined once here so those paths cannot drift apart.
			nodeAccessPolicy := httpapi.NewNodeAccessPolicy(accountsService)
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
			// Titles are user content: the center keeps them only when the operator
			// explicitly opts in (design §6/§7). The default keeps the observation
			// projection to session id + running state.
			eventStore.SetIncludeTitles(cfg.Relay.Observation.IncludeTitles)
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

			bridge := centerBridge(cfg, selfNode)
			// The watch surface needs the node proxy handler that is created further
			// down, but that handler wraps apiHandler, which needs this holder — hence
			// a holder installed after both exist.
			watchRelay := httpapi.NewWatchRelayHolder()
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
				Accounts:        accountsService,
				Metering:        meteringService,
				ControlRegistry: &registryWithOverview{Registry: controlRegistry, EventStore: eventStore, Hub: relayHub, Bridge: bridge},
				// Same bridge the proxy handler above uses: the watch surface forwards
				// remote-control requests for other nodes through the center.
				CenterBridge: bridge,
				// Reuse this instance's own node proxy handler for wrist forwarding. It
				// already knows the local-vs-center rule, so a watch paired with the
				// center still drives nodes connected to it (the center has no
				// center_url of its own to forward through).
				WatchRelay: watchRelay,
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
			if bridge != nil {
				// Center-bridge fallback: tunnels targeting nodes that joined the
				// center but are not connected to the local hub are dialed
				// through the center's forward endpoint.
				centerURL, centerToken := bridge.Endpoint()
				forwardServer.SetCenterBridge(centerURL, centerToken)
			}
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
			// NodeAccess seam (P0 §4.3/§5): every seam below asks the same "may THIS
			// caller reach THAT node" question, so one factory builds the decision
			// function and tags it with the surface for the audit trail. Writing it
			// per surface is how §1.3's hardening landed on the relay proxy while the
			// managed-forward CRUD kept answering to any signed-in tenant user.
			resolvePrincipal := httpapi.NewPrincipalResolver(manager, accountsService, controlRegistry)
			authorizeNodeFor := newNodeAccessAuthorizer(resolvePrincipal, controlRegistry, nodeAccessPolicy, accountsService)
			registerForwardRoutes(root, forwardServer, manager, nodeProxyAuthorize(cfg, accountsService), authorizeNodeFor("forward"))
			proxy := relay.NewProxyHandler(relayHub, nodeProxyAuthorize(cfg, accountsService))
			// Meter every forwarded request (design §7 basic tier). Extracted to a
			// helper so this nil check does not push main over its complexity budget.
			wireMetering(proxy, meteringService)
			// The center also runs as its own node: requests targeting the self
			// node are served locally (no relay round-trip), so the server can be
			// operated from its own web UI. These local-direct requests bypass the
			// relay agent that normally injects X-Godex-Relay-Trusted, so inject
			// the center's own web token here: sandbox/terminal endpoints behind
			// `protected` then accept the session (the caller already passed
			// nodeProxyAuthorize on the way in).
			proxy.SetLocalHandler(selfNode.ID, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if token := strings.TrimSpace(manager.Current().WebToken); token != "" {
					r.Header.Set("Authorization", "Bearer "+token)
				}
				apiHandler.ServeHTTP(w, r)
			}))
			// guarded-remote nodes require an explicit approval header on
			// mutating requests; resolve trust level from the registry.
			proxy.TrustLevel = func(nodeID string) string {
				node, err := controlRegistry.Get(context.Background(), nodeID)
				if err != nil {
					return ""
				}
				return node.TrustLevel
			}
			proxy.AuthorizeNode = authorizeNodeFor("proxy")
			bridgeProxy := httpapi.NewBridgeProxyHandler(proxy, bridge, func(nodeID string) bool {
				// Local node: the self node (served in-process) or any node
				// currently connected to the local relay hub. Everything else
				// is forwarded to the center bridge.
				if nodeID == selfNode.ID {
					return true
				}
				return relayHub.IsOnline(nodeID)
			})
			root.Handle("/control/nodes/{id}/proxy/", bridgeProxy)
			// Hand the same handler to the watch surface. This is what lets a wrist
			// paired with the CENTER drive a node connected to it: the center has no
			// center_url of its own, so its bridge is disabled and the old code
			// answered 503 — while the target was reachable one hop away.
			watchRelay.Set(bridgeProxy)
			// TCP forward surface: same NodeAccess seam as the HTTP proxy. Without
			// it a valid credential could open forward streams into any node's
			// network services, which is the §1.3 hole on a second surface.
			forwardHandler := relay.NewForwardHandler(relayHub, nodeProxyAuthorize(cfg, accountsService))
			forwardHandler.AuthorizeNode = proxy.AuthorizeNode
			root.Handle("/control/nodes/{id}/forward", forwardHandler)
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
				service,
				channelManager,
				cronService,
				heartbeatService,
			}
			// Node-side relay components (remote heartbeat / relay agent /
			// observer) are owned by controlRT so config edits can hot-reload
			// them; they are intentionally not part of the static lifecycle.
			controlRT = &controlRuntime{
				ctx:           ctx,
				apiHandler:    apiHandler,
				service:       service,
				selfEndpoint:  selfEndpoint,
				bridge:        bridge,
				forwardServer: forwardServer,
			}
			// Initial boot: start whatever the current config wants (join or no-op).
			controlRT.Reconcile(cfg, cfg)
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

// openAccountsStore opens the multi-tenant account store (tenants, users,
// memberships, login sessions).
//
// A failure must not take the whole process down: the shared web token still
// works and every existing deployment keeps functioning, so sign-in simply
// degrades to unavailable and the reason is logged rather than swallowed. The
// cleanup is returned instead of deferred here, because a defer inside this
// helper would close the store the instant it returned.
func openAccountsStore(cfg *config.Config) (*accounts.Service, func()) {
	service, err := accounts.New(cfg.StateDir)
	if err != nil {
		logger.Warnf("accounts store unavailable, email/password sign-in disabled: %v", err)
		return nil, func() {}
	}
	return service, func() { _ = service.Close() }
}

// wireMetering points the relay proxy's usage hook at the metering store.
//
// The adaptation lives here, at the composition point, so package relay keeps no
// storage dependency and does not need to know the metering package's types.
func wireMetering(proxy *relay.ProxyHandler, meteringService *metering.Service) {
	if proxy == nil || meteringService == nil {
		return
	}
	proxy.UsageRecorder = func(usage relay.ForwardUsage) {
		meteringService.RecordQuiet(metering.Record{
			TenantID:      usage.TenantID,
			UserID:        usage.UserID,
			NodeID:        usage.NodeID,
			Method:        usage.Method,
			Path:          usage.Path,
			Status:        usage.Status,
			RequestBytes:  usage.RequestBytes,
			ResponseBytes: usage.ResponseBytes,
		})
	}
}

// openMeteringStore opens the relay metering store (design §7).
//
// Degrades to nil rather than failing: metering is accounting, and an instance
// that cannot open the counter store should still forward traffic (the same shape
// as the accounts store above). The reconciliation routes answer 503 in that case,
// so the absence is visible instead of silently reporting zero usage.
func openMeteringStore(cfg *config.Config) (*metering.Service, func()) {
	service, err := metering.New(cfg.StateDir)
	if err != nil {
		logger.Warnf("metering store unavailable, relay usage reconciliation disabled: %v", err)
		return nil, func() {}
	}
	return service, func() { _ = service.Close() }
}

// splitDebugArgs extracts leading global debug flags before the command.
// Flags after the first command token remain available to that command's
// own flag set.
func splitDebugArgs(args []string) (debug []string, remainder []string) {
	for index, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--pprof-addr="),
			strings.HasPrefix(arg, "--dump-dir="),
			strings.HasPrefix(arg, "--heap-dump="):
			debug = append(debug, arg)
		default:
			remainder = append(remainder, args[index:]...)
			return debug, remainder
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
// relay hub so deleting a node can drop its live relay connection, and an
// optional center bridge so the local node list is merged with the center's
// nodes and overviews of center-reachable nodes are forwarded to the center.
type registryWithOverview struct {
	*noderegistry.Registry
	*relay.EventStore
	Hub    *relay.Hub
	Bridge *httpapi.CenterBridge
}

// List merges the local registry view with the center's node list when a
// center bridge is configured. A center fetch failure degrades to the local
// list only, so the UI keeps working when the center is unreachable.
func (r *registryWithOverview) List(ctx context.Context) ([]noderegistry.NodeView, error) {
	local, err := r.Registry.List(ctx)
	if err != nil {
		return nil, err
	}
	if r.Bridge == nil || !r.Bridge.Enabled() {
		return local, nil
	}
	remote, err := r.Bridge.ListNodes(ctx)
	if err != nil {
		return local, nil
	}
	return httpapi.MergeNodeViews(local, remote), nil
}

// Get falls back to the center bridge when the node is not in the local
// registry, so center-reachable nodes resolve for detail/overview requests.
func (r *registryWithOverview) Get(ctx context.Context, id string) (noderegistry.NodeView, error) {
	n, err := r.Registry.Get(ctx, id)
	if err == nil {
		return n, nil
	}
	if r.Bridge != nil && r.Bridge.Enabled() {
		if rn, rerr := r.Bridge.GetNode(ctx, id); rerr == nil {
			rn.Source = "center"
			return rn, nil
		}
	}
	return noderegistry.NodeView{}, os.ErrNotExist
}

// Overview serves the local observation store first, then falls back to the
// center's aggregated view for nodes reachable only through the bridge.
func (r *registryWithOverview) Overview(nodeID string) (relay.NodeOverview, bool) {
	if ov, ok := r.EventStore.Overview(nodeID); ok {
		return ov, true
	}
	if r.Bridge != nil && r.Bridge.Enabled() {
		if ov, err := r.Bridge.GetOverview(context.Background(), nodeID); err == nil {
			return ov, true
		}
	}
	return relay.NodeOverview{}, false
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

// centerBridge returns the local→center bridge client for this instance. It
// is never nil: when the center URL is unset or points at this node itself, it
// returns a bridge with an empty endpoint (Enabled() == false) so the hot-apply
// path can later SetEndpoint() it into life without replacing references held
// by the registry / proxy handler / forward server. The bridge lets the local
// Web UI reach other nodes through the center: node-scoped requests are
// forwarded to the center's proxy endpoint, which relays them over the target's
// outbound relay channel.
func centerBridge(cfg *config.Config, selfNode noderegistry.NodeInput) *httpapi.CenterBridge {
	centerURL := strings.TrimRight(strings.TrimSpace(cfg.Control.CenterURL), "/")
	if centerURL == "" || (selfNode.Endpoint != "" && strings.EqualFold(centerURL, strings.TrimRight(selfNode.Endpoint, "/"))) {
		// Not configured, or this node IS the center: an empty bridge that stays
		// disabled until SetEndpoint points it at a real center.
		return httpapi.NewCenterBridge("", "")
	}
	return httpapi.NewCenterBridge(centerURL, strings.TrimSpace(cfg.Control.CenterToken))
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
		return bearerMatches(r, token)
	}
}

// nodeProxyAuthorize accepts the full web token, the center's restricted node
// proxy token (control.node_proxy_token, nk_...), or a valid login session. The
// proxy / forward surfaces are what joined nodes call through the center bridge; a
// compromised node holding only the nk_ token can reach other nodes' proxies and
// forward probes but NOT the center's config / sessions / files management API.
//
// A login session is accepted because these are the surfaces the Web UI uses once
// a remote node is selected (design §3.5: a tenant forwards to its OWN nodes).
// Accepting it here is only safe because the caller resolves identity the same way
// and AuthorizeNode then applies NodeAccess — this gate answers "is there a valid
// credential", not "may this caller reach this node".
func nodeProxyAuthorize(cfg *config.Config, accountsService *accounts.Service) func(*http.Request) bool {
	sessionChecker := httpapi.NewSessionTokenChecker(accountsService)
	return func(r *http.Request) bool {
		token := strings.TrimSpace(cfg.WebToken)
		proxyToken := strings.TrimSpace(cfg.Control.NodeProxyToken)
		if token == "" && proxyToken == "" {
			// No operator credential configured: this deployment is open, which is
			// the pre-existing behaviour, and a session must not be the only way in.
			return true
		}
		if bearerMatches(r, token) || bearerMatches(r, proxyToken) {
			return true
		}
		return sessionChecker != nil && sessionChecker(r)
	}
}

func bearerMatches(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(strings.ToLower(header), "bearer ") {
		return false
	}
	return strings.TrimSpace(header[len("Bearer "):]) == token
}

// nodeGetter is the slice of the node registry the authorization seam needs.
// Narrow on purpose so the decision function can be tested without a registry.
type nodeGetter interface {
	Get(ctx context.Context, id string) (noderegistry.NodeView, error)
}

// newNodeAccessAuthorizer builds the single "may this caller reach that node"
// decision every seam in this process shares (design §4.3/§5).
//
// The surface argument only labels the delegated-read audit row, so the trail
// names the path that was exercised (proxy vs forward) rather than a generic
// entry. Everything security-relevant is identical across surfaces on purpose:
// one policy, one principal resolver, one decision.
func newNodeAccessAuthorizer(
	resolvePrincipal func(*http.Request) identity.Principal,
	registry nodeGetter,
	policy identity.Policy,
	accountsService *accounts.Service,
) func(surface string) func(*http.Request, string) identity.Decision {
	return func(surface string) func(*http.Request, string) identity.Decision {
		return func(r *http.Request, nodeID string) identity.Decision {
			principal := resolvePrincipal(r)
			node, err := registry.Get(context.Background(), nodeID)
			if err != nil {
				// Unknown node: the hub will report it as offline. Authorizing
				// here would mean inventing ownership for a node we cannot see,
				// so let it through and let the transport answer.
				return identity.AllowedDecision(identity.ReasonAllowed)
			}
			// The SAME policy the HTTP read surface uses, including the superadmin's
			// tenant sharing switch: building a bare Policy here would let a proxied
			// request reach a node that the direct read path just refused, which is
			// exactly the silent bypass this seam exists to prevent.
			decision := policy.Authorize(principal, node.NodeRef())
			// Audit the delegated case HERE as well as on the read routes (§3.6):
			// these are the surfaces the web UI drives, so auditing only the
			// direct read routes would leave the trail empty in precisely the
			// scenario it exists for.
			httpapi.RecordDelegatedRead(accountsService, r, decision, surface+" "+nodeID)
			return decision
		}
	}
}

// registerForwardRoutes wires the managed-forward REST surface onto the root
// mux (external paths carry the /api prefix the webui strips):
//
//	GET    /control/forwards           list tunnels (masked to reachable nodes)
//	POST   /control/forwards           create a tunnel (persisted to config)
//	DELETE /control/forwards/{id}      remove a tunnel (persisted)
//	POST   /control/forwards/{id}/check  probe the tunnel end to end
//
// The credential gate alone is not enough: it accepts any login session, so
// without the per-target node check a member of tenant A could open a tunnel
// into tenant B's node (design §1.3). authorizeNode is the SAME NodeAccess
// decision the relay proxy uses, so the two surfaces cannot disagree.
func registerForwardRoutes(mux *http.ServeMux, server *relay.ForwardServer, manager *config.Manager, authorize func(*http.Request) bool, authorizeNode func(*http.Request, string) identity.Decision) {
	guard := func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authorize != nil && !authorize(r) {
				http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			next(w, r)
		})
	}
	// denyNode writes 403 and reports true when the caller may not reach nodeID.
	// A nil authorizeNode keeps the previous behaviour (no node scoping).
	// denyNode writes a refusal and reports true when the caller may not reach nodeID.
	//
	// A nil authorizeNode DENIES rather than allows. "No node check configured"
	// must not degrade into "every node is reachable": that fail-open shape is
	// precisely how this surface lost tenant isolation in the first place, so the
	// wiring mistake must be loud and safe instead of silent and permissive.
	denyNode := func(w http.ResponseWriter, r *http.Request, nodeID string) bool {
		if authorizeNode == nil {
			http.Error(w, `{"error":"node authorization is not configured"}`, http.StatusServiceUnavailable)
			return true
		}
		decision := authorizeNode(r, nodeID)
		if decision.Allowed {
			return false
		}
		http.Error(w, `{"error":"`+jsonEscape("node access denied: "+decision.Reason)+`"}`, http.StatusForbidden)
		return true
	}

	mux.Handle("GET /control/forwards", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mask rather than refuse: a caller sees the tunnels they may reach, which
		// is the same shape the node list uses. Refusing the whole list would make
		// the page unusable for a tenant that manages its own nodes.
		writeForwardJSON(w, http.StatusOK, visibleForwards(server.List(), authorizeNode, r))
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
		nodeID := strings.TrimSpace(req.NodeID)
		// Authorize BEFORE Add: otherwise the listener is already bound to the
		// other tenant's node by the time the response says no.
		if denyNode(w, r, nodeID) {
			return
		}
		spec, err := server.Add(relay.ForwardSpec{
			Name:      strings.TrimSpace(req.Name),
			NodeID:    nodeID,
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
		// Audit: forward tunnel creation (P0-2).
		logger.Infof("audit forward create: caller=%s node=%s local=%d target=%s id=%s", r.RemoteAddr, spec.NodeID, spec.LocalPort, spec.Target, spec.ID)
		writeForwardJSON(w, http.StatusCreated, spec)
	})))

	mux.Handle("DELETE /control/forwards/{id}", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// Resolve the tunnel's node BEFORE removing it: deleting is a write to that
		// node's exposure, so it needs the same decision as creating one.
		if status, ok := server.Get(id); ok && denyNode(w, r, status.NodeID) {
			return
		}
		if !server.Remove(id) {
			http.Error(w, `{"error":"forward not found"}`, http.StatusNotFound)
			return
		}
		if err := persistForwards(r.Context(), manager, server); err != nil {
			http.Error(w, `{"error":"persist failed"}`, http.StatusInternalServerError)
			return
		}
		// Audit: forward tunnel removal (P0-2).
		logger.Infof("audit forward delete: caller=%s id=%s", r.RemoteAddr, id)
		writeForwardJSON(w, http.StatusOK, map[string]bool{"removed": true})
	})))

	mux.Handle("POST /control/forwards/{id}/check", guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		// A probe opens a real connection to the target over the node's network,
		// so it is a reach into that node and is authorized the same way.
		if status, ok := server.Get(id); ok && denyNode(w, r, status.NodeID) {
			return
		}
		result, err := server.Check(id)
		if err != nil {
			http.Error(w, `{"error":"forward not found"}`, http.StatusNotFound)
			return
		}
		writeForwardJSON(w, http.StatusOK, result)
	})))
}

// visibleForwards returns the tunnels the caller may reach. A nil authorizeNode
// means node scoping is not configured, so every tunnel is visible (the
// pre-existing behaviour).
// visibleForwards returns the tunnels the caller may reach. A nil authorizeNode
// yields an empty list for the same reason denyNode refuses: listing what could
// not be reached would leak the node ids the decision exists to withhold.
func visibleForwards(list []relay.ForwardStatus, authorizeNode func(*http.Request, string) identity.Decision, r *http.Request) []relay.ForwardStatus {
	out := make([]relay.ForwardStatus, 0, len(list))
	if authorizeNode == nil {
		return out
	}
	for _, status := range list {
		if authorizeNode(r, status.NodeID).Allowed {
			out = append(out, status)
		}
	}
	return out
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
	case "doctor", "docs", "help", "-h", "--help", "login", "logout", "migrate", "node", "providers", "repair", "service", "tui", "version", "--version":
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
