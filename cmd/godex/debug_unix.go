//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// installSignalDumpHandlers registers handlers that, on SIGQUIT, write
// goroutine dumps and, on SIGUSR1, write heap dumps.
func installSignalDumpHandlers(f debugFlags) {
	dir := f.DumpDir
	if strings.TrimSpace(dir) == "" {
		dir = defaultDumpDir(os.TempDir(), os.Getpid())
	}
	pid := os.Getpid()
	signalDumpOnce(func() (string, error) { return writeGoroutineDump(dir, pid) })
	signalHeapOnce(f.HeapDumpPath)
}

// signalDumpOnce registers a SIGQUIT handler that, on every receipt,
// invokes fn and prints the resulting file path to stderr.
func signalDumpOnce(fn func() (string, error)) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGQUIT)
	go func() {
		for range ch {
			path, err := fn()
			if err != nil {
				fmt.Fprintf(os.Stderr, "godex: dump failed: %v\n", err)
				continue
			}
			fmt.Fprintf(os.Stderr, "godex: goroutine dump written to %s\n", path)
		}
	}()
}

// signalHeapOnce installs a SIGUSR1 handler that calls debug.WriteHeapDump.
// A pre-set path triggers a one-shot dump at startup instead.
func signalHeapOnce(preSetPath string) {
	if strings.TrimSpace(preSetPath) != "" {
		if err := writeHeapDumpTo(preSetPath); err != nil {
			fmt.Fprintf(os.Stderr, "godex: heap dump failed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "godex: heap dump written to %s\n", preSetPath)
		}
		return
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGUSR1)
	go func() {
		for range ch {
			path := filepath.Join(os.TempDir(), fmt.Sprintf("godex-heap-%d-%d.out", os.Getpid(), time.Now().Unix()))
			if err := writeHeapDumpTo(path); err != nil {
				fmt.Fprintf(os.Stderr, "godex: heap dump failed: %v\n", err)
				continue
			}
			fmt.Fprintf(os.Stderr, "godex: heap dump written to %s\n", path)
		}
	}()
}
