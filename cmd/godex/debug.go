package main

import (
	"fmt"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

// debugFlags configures optional diagnostic behaviour for the godex
// binary. All flags are off by default; turning them on costs very little
// for normal usage and gives operators a way to recover a clean
// goroutine dump even when the TUI alt-screen renderer is corrupting
// the terminal.
type debugFlags struct {
	// PprofAddr is the listen address for the net/http/pprof endpoint,
	// e.g. ":6060". Empty means pprof is disabled.
	PprofAddr string

	// DumpDir is the directory where SIGQUIT-triggered goroutine dumps
	// are written. Empty falls back to os.TempDir()/godex-dumps at the
	// moment the handler is registered.
	DumpDir string

	// HeapDumpPath, if non-empty, triggers a one-shot
	// runtime/debug.WriteHeapDump at startup. Use a path the operator
	// can read; defaults to <DumpDir>/heap-<pid>-<time>.out when empty
	// in conjunction with DumpDir.
	HeapDumpPath string
}

// parseDebugFlags walks args looking for --pprof-addr=, --dump-dir=,
// and --heap-dump=. Unknown debug flags are rejected so typos don't
// silently disable a feature an operator thought they had turned on.
func parseDebugFlags(args []string) (debugFlags, error) {
	f := debugFlags{}
	for _, arg := range args {
		switch {
		case strings.HasPrefix(arg, "--pprof-addr="):
			f.PprofAddr = strings.TrimSpace(strings.TrimPrefix(arg, "--pprof-addr="))
		case strings.HasPrefix(arg, "--dump-dir="):
			f.DumpDir = strings.TrimSpace(strings.TrimPrefix(arg, "--dump-dir="))
		case strings.HasPrefix(arg, "--heap-dump="):
			f.HeapDumpPath = strings.TrimSpace(strings.TrimPrefix(arg, "--heap-dump="))
		default:
			return debugFlags{}, fmt.Errorf("unknown debug flag: %q", arg)
		}
	}
	return f, nil
}

// defaultDumpDir joins tempDir/godex-dumps so multiple godex processes
// on the same host don't overwrite each other's dumps.
func defaultDumpDir(tempDir string, pid int) string {
	_ = pid
	return filepath.Join(tempDir, "godex-dumps")
}

// writeGoroutineDump writes a complete goroutine dump to a fresh file
// inside dir. The path is returned so the caller can log it. If dir
// does not exist it is created with 0700 permissions.
//
// The dump file is the result of runtime.Stack with all=true, which
// gives one stack per goroutine in a format identical to what SIGQUIT
// writes to stderr. Critically, writing to a file is unaffected by the
// alt-screen renderer that corrupts the terminal when the TUI is hung.
func writeGoroutineDump(dir string, pid int) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	buf := make([]byte, 1<<20) // 1 MiB; grows on demand below
	for {
		n := runtime.Stack(buf, true)
		if n < len(buf) {
			buf = buf[:n]
			break
		}
		// Buffer was too small; grow and retry.
		buf = make([]byte, len(buf)*2)
	}
	stamp := time.Now().UTC().Format("20060102T150405Z")
	path := filepath.Join(dir, fmt.Sprintf("godex-%d-%s.dump", pid, stamp))
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// startPprofServer binds the net/http/pprof handler tree to addr. The
// pprof import registers handlers on http.DefaultServeMux, so we
// simply start a server. An empty addr is a no-op so the main routine
// can call this unconditionally based on a flag value.
func startPprofServer(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("pprof listen %s: %w", addr, err)
	}
	go func() {
		srv := &http.Server{
			Handler:      http.DefaultServeMux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 30 * time.Second,
		}
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "godex: pprof server exited: %v\n", err)
		}
	}()
	fmt.Fprintf(os.Stderr, "godex: pprof listening on http://%s/debug/pprof/\n", addr)
	return nil
}

// pidString is a tiny helper for log lines.
func pidString() string { return strconv.Itoa(os.Getpid()) }

// writeHeapDumpTo triggers a heap dump at the given path. It is split
// out so tests can drive a deterministic path and so signal handlers
// share a single implementation.
func writeHeapDumpTo(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	debug.WriteHeapDump(f.Fd())
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return nil
}
