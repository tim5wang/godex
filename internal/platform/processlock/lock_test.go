package processlock

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const processLockHelperEnv = "GODEX_PROCESS_LOCK_HELPER_PATH"

func TestAcquireExcludesOtherProcessesAndCrashReleasesLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.lock")
	cmd := exec.Command(os.Args[0], "-test.run=^TestProcessLockHelper$")
	cmd.Env = append(os.Environ(), processLockHelperEnv+"="+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("open child stdout: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start lock holder: %v", err)
	}
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	ready := make(chan error, 1)
	go func() {
		line, err := bufio.NewReader(stdout).ReadString('\n')
		if err == nil && line != "locked\n" {
			err = fmt.Errorf("unexpected lock-holder output %q", line)
		}
		ready <- err
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("wait for child lock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("lock holder did not become ready")
	}

	if _, err := Acquire(path); !errors.Is(err, ErrLocked) {
		t.Fatalf("expected a competing process lock to fail with ErrLocked, got %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("terminate lock holder: %v", err)
	}
	_ = cmd.Wait()
	waited = true

	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("process exit should release the lock: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("close lock: %v", err)
	}
}

func TestProcessLockHelper(t *testing.T) {
	path := os.Getenv(processLockHelperEnv)
	if path == "" {
		return
	}
	lock, err := Acquire(path)
	if err != nil {
		t.Fatalf("acquire child lock: %v", err)
	}
	defer lock.Close()
	fmt.Fprintln(os.Stdout, "locked")
	select {}
}
