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
