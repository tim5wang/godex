package main

import (
	"context"
	"fmt"
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
	pkgregistry "github.com/tim5wang/godex/internal/core/packages"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/platform/servicecontrol"
	rtchannels "github.com/tim5wang/godex/internal/runtime/channels"
	"github.com/tim5wang/godex/internal/runtime/channels/feishu"
	"github.com/tim5wang/godex/internal/runtime/channels/weixin"
	rtcron "github.com/tim5wang/godex/internal/runtime/cron"
	rtheartbeat "github.com/tim5wang/godex/internal/runtime/heartbeat"
	"github.com/tim5wang/godex/internal/runtime/httpapi"
	"github.com/tim5wang/godex/internal/runtime/repl"
	"github.com/tim5wang/godex/internal/runtime/webui"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
	"github.com/tim5wang/godex/internal/services/evalharness"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/sessionadmin"
	"github.com/tim5wang/godex/internal/services/usage"
	"github.com/tim5wang/godex/internal/tui"
	"github.com/tim5wang/godex/internal/version"
)

func main() {
	configOptions, args, err := extractGlobalConfigArgs(os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	if len(args) > 0 && (args[0] == "setup" || args[0] == "init") {
		if err := app.RunSetupCommand(context.Background(), args[1:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
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
	channelManager := rtchannels.NewManager(cfg, service)
	cronService := rtcron.NewService(rtcron.Config{
		Enabled:           cfg.Cron.Enabled,
		TickSeconds:       cfg.Cron.TickSeconds,
		DefaultTimezone:   cfg.Cron.DefaultTimezone,
		MaxConcurrentRuns: cfg.Cron.MaxConcurrentRuns,
	}, rtcron.NewFileStore(cfg.StateDir), service, channelManager)
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
	}, rtheartbeat.NewFileStore(cfg.StateDir), service, channelManager)
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
		Cfg:           cfg,
		ConfigManager: manager,
		Backend:       service,
		Stdin:         os.Stdin,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
		RunREPL: func(ctx context.Context) error {
			return repl.New(cfg, service, os.Stdout, os.Stderr).Run(ctx)
		},
		RunTUI: func(ctx context.Context, locator backend.SessionLocator) error {
			return tui.New(cfg, service, os.Stdout).Run(ctx, locator)
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
			apiHandler := httpapi.NewHandlerWithRuntime(manager, service, channelManager, weixinAuth, cronToolAdapter, heartbeatToolAdapter, serviceRuntimeControl{
				controller: servicecontrol.NewController(),
				options:    serviceRuntimeOptions(manager),
			}, usageService, controlRegistry)
			handler, err := webui.NewHandler(apiHandler, filepath.Join(cfg.WorkspaceDir, "internal", "uiassets", "embedded_dist"))
			if err != nil {
				return err
			}

			lifecycle := []app.LifecycleService{
				app.NewConfigReloadWatcher(manager),
				noderegistry.NewLocalHeartbeat(controlRegistry, selfNode, time.Duration(cfg.Control.HeartbeatSeconds)*time.Second),
				channelManager,
				cronService,
				heartbeatService,
			}
			if remote := remoteControlHeartbeat(cfg, selfNode, selfEndpoint); remote != nil {
				lifecycle = append(lifecycle, remote)
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
		default:
			remaining = append(remaining, arg)
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
	case "doctor", "help", "-h", "--help", "login", "logout", "migrate", "providers", "repair", "service", "version", "--version":
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
	}
}
