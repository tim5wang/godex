package servicecontrol

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNotifyServiceSendsReadyWatchdogAndStopping(t *testing.T) {
	socketPath := fmt.Sprintf("/tmp/godex-notify-%d.sock", time.Now().UnixNano())
	defer os.Remove(socketPath)
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen unixgram: %v", err)
	}
	defer conn.Close()

	service := NewNotifyService(NotifyOptions{
		SocketPath:       socketPath,
		WatchdogInterval: 20 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := service.Start(ctx); err != nil {
		t.Fatalf("start notify service: %v", err)
	}
	defer service.Stop(context.Background())

	waitForNotifyMessage(t, conn, "READY=1")
	waitForNotifyMessage(t, conn, "WATCHDOG=1")

	if err := service.Stop(context.Background()); err != nil {
		t.Fatalf("stop notify service: %v", err)
	}
	waitForNotifyMessage(t, conn, "STOPPING=1")
}

func waitForNotifyMessage(t *testing.T, conn *net.UnixConn, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	buf := make([]byte, 1024)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			t.Fatalf("set read deadline: %v", err)
		}
		n, _, err := conn.ReadFromUnix(buf)
		if err != nil {
			if os.IsTimeout(err) {
				continue
			}
			t.Fatalf("read notify message: %v", err)
		}
		if strings.Contains(string(buf[:n]), want) {
			return
		}
	}
	t.Fatalf("timed out waiting for notify message %q", want)
}
