//go:build windows

package main

// installSignalDumpHandlers is a no-op on Windows (SIGQUIT/SIGUSR1 not available).
func installSignalDumpHandlers(f debugFlags) {}

// signalDumpOnce is a no-op on Windows.
func signalDumpOnce(fn func() (string, error)) {}

// signalHeapOnce is a no-op on Windows except for pre-set path dumps.
func signalHeapOnce(preSetPath string) {}
