package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/servicecontrol"
)

type serviceRuntime interface {
	Install(context.Context, servicecontrol.InstallOptions) (servicecontrol.Status, error)
	Uninstall(context.Context, servicecontrol.InstallOptions) (servicecontrol.Status, error)
	Start(context.Context, servicecontrol.InstallOptions) (servicecontrol.Status, error)
	Stop(context.Context, servicecontrol.InstallOptions) (servicecontrol.Status, error)
	Restart(context.Context, servicecontrol.InstallOptions) (servicecontrol.Status, error)
	Status(context.Context, servicecontrol.InstallOptions) (servicecontrol.Status, error)
	Logs(context.Context, servicecontrol.InstallOptions, bool, io.Writer, io.Writer) error
}

func (r *Runner) runService(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing service subcommand\n\n%s", serviceHelpText())
	}
	runtime := serviceRuntime(servicecontrol.NewController())
	switch args[0] {
	case "install":
		opts, err := r.parseServiceOptions(args[0], args[1:], true)
		if err != nil {
			return err
		}
		status, err := runtime.Install(ctx, opts)
		printServiceStatus(r.Stdout, "Installed", status)
		return err
	case "uninstall":
		opts, err := r.parseServiceOptions(args[0], args[1:], false)
		if err != nil {
			return err
		}
		status, err := runtime.Uninstall(ctx, opts)
		printServiceStatus(r.Stdout, "Uninstalled", status)
		return err
	case "start":
		opts, err := r.parseServiceOptions(args[0], args[1:], false)
		if err != nil {
			return err
		}
		status, err := runtime.Start(ctx, opts)
		printServiceStatus(r.Stdout, "Started", status)
		return err
	case "stop":
		opts, err := r.parseServiceOptions(args[0], args[1:], false)
		if err != nil {
			return err
		}
		status, err := runtime.Stop(ctx, opts)
		printServiceStatus(r.Stdout, "Stopped", status)
		return err
	case "restart":
		opts, err := r.parseServiceOptions(args[0], args[1:], false)
		if err != nil {
			return err
		}
		status, err := runtime.Restart(ctx, opts)
		printServiceStatus(r.Stdout, "Restarted", status)
		return err
	case "status":
		opts, err := r.parseServiceOptions(args[0], args[1:], false)
		if err != nil {
			return err
		}
		status, err := runtime.Status(ctx, opts)
		printServiceStatus(r.Stdout, "Status", status)
		return err
	case "logs":
		opts, follow, err := r.parseServiceLogsOptions(args[1:])
		if err != nil {
			return err
		}
		return runtime.Logs(ctx, opts, follow, r.Stdout, r.Stderr)
	case "help", "-h", "--help":
		fmt.Fprintln(r.Stdout, serviceHelpText())
		return nil
	default:
		return fmt.Errorf("unknown service subcommand %q\n\n%s", args[0], serviceHelpText())
	}
}

func (r *Runner) parseServiceOptions(command string, args []string, includeAddr bool) (servicecontrol.InstallOptions, error) {
	fs := flag.NewFlagSet("service "+command, flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	scope := "user"
	name := "godex"
	addr := "127.0.0.1:8088"
	fs.StringVar(&scope, "scope", scope, "service scope: user or system")
	fs.StringVar(&name, "name", name, "service name")
	if includeAddr {
		fs.StringVar(&addr, "addr", addr, "HTTP listen address for godex serve")
	}
	if err := fs.Parse(args); err != nil {
		return servicecontrol.InstallOptions{}, err
	}
	if len(fs.Args()) > 0 {
		return servicecontrol.InstallOptions{}, fmt.Errorf("unexpected service %s arguments: %s", command, strings.Join(fs.Args(), " "))
	}
	opts := r.serviceOptions(name, scope)
	if includeAddr {
		opts.Addr = addr
	}
	return opts, nil
}

func (r *Runner) parseServiceLogsOptions(args []string) (servicecontrol.InstallOptions, bool, error) {
	fs := flag.NewFlagSet("service logs", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	scope := "user"
	name := "godex"
	follow := false
	fs.StringVar(&scope, "scope", scope, "service scope: user or system")
	fs.StringVar(&name, "name", name, "service name")
	fs.BoolVar(&follow, "follow", follow, "follow log output")
	if err := fs.Parse(args); err != nil {
		return servicecontrol.InstallOptions{}, false, err
	}
	if len(fs.Args()) > 0 {
		return servicecontrol.InstallOptions{}, false, fmt.Errorf("unexpected service logs arguments: %s", strings.Join(fs.Args(), " "))
	}
	return r.serviceOptions(name, scope), follow, nil
}

func (r *Runner) serviceOptions(name, scope string) servicecontrol.InstallOptions {
	cfg := r.currentConfig()
	meta := config.Meta{}
	if r.ConfigManager != nil {
		meta = r.ConfigManager.Meta()
	}
	workingDir := firstNonEmpty(meta.ProjectDir, cfg.ProjectDir, cfg.WorkspaceDir)
	homeDir := firstNonEmpty(meta.HomeDir, cfg.HomeDir)
	projectDir := firstNonEmpty(meta.ProjectDir, cfg.ProjectDir, cfg.WorkspaceDir)
	logPath := cfg.Logging.FilePath
	if logPath == "" && homeDir != "" {
		logPath = filepath.Join(homeDir, "log", "godex.service.log")
	}
	binaryPath, _ := os.Executable()
	return servicecontrol.InstallOptions{
		Name:       name,
		Scope:      servicecontrol.Scope(scope),
		BinaryPath: binaryPath,
		WorkingDir: workingDir,
		HomeDir:    homeDir,
		ProjectDir: projectDir,
		LogPath:    logPath,
	}
}

func printServiceStatus(stdout io.Writer, action string, status servicecontrol.Status) {
	fmt.Fprintf(stdout, "%s GoDex service %q (%s scope)\n", action, status.Name, status.Scope)
	if status.ServiceFile != "" {
		fmt.Fprintf(stdout, "Service: %s\n", status.ServiceFile)
	}
	if status.LogFile != "" {
		fmt.Fprintf(stdout, "Log: %s\n", status.LogFile)
	}
	fmt.Fprintf(stdout, "Installed: %v\n", status.Installed)
	fmt.Fprintf(stdout, "Running: %v\n", status.Running)
	if status.Detail != "" {
		fmt.Fprintf(stdout, "Detail: %s\n", truncateServiceDetail(status.Detail))
	}
}

func serviceHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex service install [--scope user|system] [--name godex] [--addr 127.0.0.1:8088]",
		"  godex service uninstall [--scope user|system] [--name godex]",
		"  godex service start [--scope user|system] [--name godex]",
		"  godex service stop [--scope user|system] [--name godex]",
		"  godex service restart [--scope user|system] [--name godex]",
		"  godex service status [--scope user|system] [--name godex]",
		"  godex service logs [--scope user|system] [--name godex] [--follow]",
		"",
		"Default scope is user. Use --scope system for a machine-level service when the OS account has permission.",
	}, "\n")
}

func truncateServiceDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if len(detail) <= 800 {
		return detail
	}
	return detail[:800] + "..."
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
