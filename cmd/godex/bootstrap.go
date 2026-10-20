package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/tim5wang/godex/internal/app"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/servicecontrol"
)

// prepareRuntimeArgs imports the interactive environment and handles the
// startup-only argument paths before long-lived services are constructed.
func prepareRuntimeArgs() (config.Options, []string, bool, error) {
	// Pure informational commands must work even with a broken config or a
	// slow login shell; they need neither environment import nor session state.
	if args := os.Args[1:]; len(args) > 0 {
		switch args[0] {
		case "version", "--version", "help", "--help", "-h":
			runner := app.Runner{Stdout: os.Stdout, Stderr: os.Stderr}
			return config.Options{}, args, true, runner.Run(context.Background(), args)
		}
	}
	envCtx, envCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := servicecontrol.ImportUserShellEnvironment(envCtx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: %v\n", err)
	}
	envCancel()

	configOptions, args, err := extractGlobalConfigArgs(os.Args[1:])
	if err != nil {
		return config.Options{}, nil, false, err
	}
	debugArgs, args := splitDebugArgs(args)
	debug, err := parseDebugFlags(debugArgs)
	if err != nil {
		return config.Options{}, nil, false, err
	}
	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "help", "--help", "-h":
			runner := app.Runner{Stdout: os.Stdout, Stderr: os.Stderr}
			return configOptions, args, true, runner.Run(context.Background(), args)
		}
	}
	if !debug.PprofAddrSet && len(args) > 0 && args[0] == "serve" {
		serviceOpts := servicecontrol.CurrentOptions()
		debug.PprofAddr, err = servicecontrol.LoadPprofAddr(os.Getenv("GODEX_HOME"), serviceOpts.Name)
		if err != nil {
			return config.Options{}, nil, false, fmt.Errorf("load service pprof setting: %w", err)
		}
	}
	if err := startPprofServer(debug.PprofAddr); err != nil {
		return config.Options{}, nil, false, err
	}
	installSignalDumpHandlers(debug)
	if len(args) > 0 && (args[0] == "setup" || args[0] == "init") {
		err := app.RunSetupCommand(context.Background(), args[1:], os.Stdout, os.Stderr)
		return configOptions, args, true, err
	}
	return configOptions, args, false, nil
}
