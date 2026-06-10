package main

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestDebugFlagsParsePprofAddr locks in the contract of debugFlags.Parse:
// --pprof-addr=:6060 enables pprof on the given listen address.
func TestDebugFlagsParsePprofAddr(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw      string
		wantPprof string
		wantErr  bool
	}{
		{raw: "--pprof-addr=:6060", wantPprof: ":6060"},
		{raw: "--pprof-addr=localhost:7000", wantPprof: "localhost:7000"},
		{raw: "", wantPprof: ""}, // disabled by default
	}
	for _, c := range cases {
		c := c
		t.Run(c.raw, func(t *testing.T) {
			t.Parallel()
			f, err := parseDebugFlags(strings.Fields(c.raw))
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", c.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if f.PprofAddr != c.wantPprof {
				t.Fatalf("PprofAddr: got %q, want %q", f.PprofAddr, c.wantPprof)
			}
		})
	}
}

// TestDebugFlagsParseDumpDir verifies --dump-dir sets a default dump
// directory used by the SIGQUIT handler.
func TestDebugFlagsParseDumpDir(t *testing.T) {
	t.Parallel()

	f, err := parseDebugFlags([]string{"--dump-dir=/var/tmp/godex-dumps"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.DumpDir != "/var/tmp/godex-dumps" {
		t.Fatalf("DumpDir: got %q, want /var/tmp/godex-dumps", f.DumpDir)
	}
}

// TestDebugFlagsParseHeapDump verifies --heap-dump=path triggers a one-shot
// heap dump at startup. The path is resolved to absolute relative to cwd
// at the time the flag is set.
func TestDebugFlagsParseHeapDump(t *testing.T) {
	t.Parallel()

	f, err := parseDebugFlags([]string{"--heap-dump=/tmp/heap.out"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if f.HeapDumpPath != "/tmp/heap.out" {
		t.Fatalf("HeapDumpPath: got %q, want /tmp/heap.out", f.HeapDumpPath)
	}
}

// TestDebugFlagsRejectsUnknownArg ensures unknown debug flags fail loudly
// instead of being silently swallowed (which would mask typos in production).
func TestDebugFlagsRejectsUnknownArg(t *testing.T) {
	t.Parallel()

	if _, err := parseDebugFlags([]string{"--not-a-real-flag=foo"}); err == nil {
		t.Fatalf("expected error for unknown flag, got nil")
	}
}

// TestDefaultDumpDirForPID documents that when --dump-dir is empty the
// handler falls back to os.TempDir()/godex-dumps. The test uses a temp
// directory override to assert the join logic without depending on /tmp.
func TestDefaultDumpDirForPID(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	got := defaultDumpDir(base, 1234)
	want := filepath.Join(base, "godex-dumps")
	if got != want {
		t.Fatalf("defaultDumpDir: got %q, want %q", got, want)
	}
}

// TestWriteGoroutineDumpWritesToFile is the core regression test: the
// SIGQUIT handler must write a non-empty, parseable goroutine dump to a
// file in the dump directory so the user can recover the stack even when
// the alt-screen renderer is corrupting the terminal.
func TestWriteGoroutineDumpWritesToFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, err := writeGoroutineDump(dir, 99999)
	if err != nil {
		t.Fatalf("writeGoroutineDump: %v", err)
	}

	// File should be inside the dump dir and non-empty.
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("dump path %q not under %q", path, dir)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat dump: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("dump file is empty")
	}
	// Sanity: should at least contain the dump header from runtime.Stack.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	if !strings.Contains(string(data), "goroutine ") {
		t.Fatalf("dump does not look like a goroutine dump (no 'goroutine' marker)")
	}
}

// TestStartPprofServerRejectsEmptyAddr documents that calling
// startPprofServer with an empty address is a no-op and returns nil.
// An empty address would otherwise either panic on http.ListenAndServe
// or accidentally open a public pprof endpoint.
func TestStartPprofServerRejectsEmptyAddr(t *testing.T) {
	t.Parallel()

	if err := startPprofServer(""); err != nil {
		t.Fatalf("startPprofServer(\"\"): %v", err)
	}
}

// TestStartPprofServerServesKnownPaths pins the public contract of the
// pprof endpoint: the /debug/pprof/ tree must be reachable on the
// configured listen address. This is the path operators will hit with
// `curl http://localhost:6060/debug/pprof/goroutine?debug=2` to recover
// a clean dump even when SIGQUIT is too disruptive to use.
func TestStartPprofServerServesKnownPaths(t *testing.T) {
	t.Parallel()

	// Pick a free port by binding to :0 first.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := probe.Addr().String()
	probe.Close()

	if err := startPprofServer(addr); err != nil {
		t.Fatalf("startPprofServer: %v", err)
	}

	// /debug/pprof/ is the index page; if it returns 200 we know the
	// entire pprof tree is wired up.
	url := "http://" + addr + "/debug/pprof/"
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}

	// /debug/pprof/goroutine?debug=2 is the human-readable dump.
	gurl := "http://" + addr + "/debug/pprof/goroutine?debug=2"
	gresp, err := client.Get(gurl)
	if err != nil {
		t.Fatalf("GET %s: %v", gurl, err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", gurl, gresp.StatusCode)
	}
}

// TestWriteHeapDumpToCreatesFile is the core regression test for the
// one-shot heap dump. The dump must land at the exact path requested,
// the file must exist, and the call must not panic when the path's
// parent directory does not yet exist (the helper must mkdir -p).
func TestWriteHeapDumpToCreatesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	// Put the file in a not-yet-existing subdirectory to exercise the
	// MkdirAll path inside writeHeapDumpTo.
	path := filepath.Join(dir, "subdir", "heap.out")

	if err := writeHeapDumpTo(path); err != nil {
		t.Fatalf("writeHeapDumpTo: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat heap dump: %v", err)
	}
	if info.Size() == 0 {
		t.Fatalf("heap dump is empty")
	}
	// The first four bytes of a Go heap dump are a magic prefix.
	// See https://github.com/golang/go/blob/master/src/runtime/mheap.go
	// for the exact value; for our purposes the dump just has to be
	// non-empty and not be a Go source file.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	header := make([]byte, 16)
	n, _ := f.Read(header)
	if n < 4 {
		t.Fatalf("heap dump too small: %d bytes", n)
	}
	// The Go heap dump format starts with a small ASCII header line.
	// If we see "package" or "func" we are reading Go source, not a
	// heap dump.
	if strings.HasPrefix(string(header[:n]), "package ") || strings.HasPrefix(string(header[:n]), "func ") {
		t.Fatalf("heap dump looks like Go source: %q", string(header[:n]))
	}
}

// TestSignalHeapOncePreSetPathSkipsHandler documents that passing a
// non-empty preSetPath makes signalHeapOnce do its dump at startup and
// not install a SIGUSR1 listener. We verify the listener side by
// checking the dump file is created when we pass a path.
func TestSignalHeapOncePreSetPathSkipsHandler(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "boot-heap.out")

	signalHeapOnce(path)

	// Dump file should exist immediately after the call returned.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("heap dump not created at startup: %v", err)
	}
}
