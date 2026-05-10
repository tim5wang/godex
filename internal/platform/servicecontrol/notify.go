package servicecontrol

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NotifyOptions controls systemd-style service readiness notifications.
type NotifyOptions struct {
	SocketPath       string
	WatchdogInterval time.Duration
}

// NotifyService sends sd_notify readiness and watchdog messages when
// NOTIFY_SOCKET is available. Outside systemd it is a no-op lifecycle service.
type NotifyService struct {
	options NotifyOptions

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewNotifyService(options NotifyOptions) *NotifyService {
	return &NotifyService{options: options}
}

func NewNotifyServiceFromEnv() *NotifyService {
	socketPath := strings.TrimSpace(os.Getenv("NOTIFY_SOCKET"))
	watchdogInterval := parseWatchdogInterval(os.Getenv("WATCHDOG_USEC"))
	return NewNotifyService(NotifyOptions{
		SocketPath:       socketPath,
		WatchdogInterval: watchdogInterval,
	})
}

func (s *NotifyService) Start(ctx context.Context) error {
	if s == nil || strings.TrimSpace(s.options.SocketPath) == "" {
		return nil
	}
	s.mu.Lock()
	if s.cancel != nil {
		s.mu.Unlock()
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	s.cancel = cancel
	s.done = done
	s.mu.Unlock()

	if err := sendNotify(s.options.SocketPath, "READY=1\nSTATUS=GoDex is ready"); err != nil {
		cancel()
		s.mu.Lock()
		s.cancel = nil
		s.done = nil
		s.mu.Unlock()
		close(done)
		return err
	}

	go s.run(runCtx, done)
	return nil
}

func (s *NotifyService) Stop(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if strings.TrimSpace(s.options.SocketPath) == "" {
		return nil
	}
	return sendNotify(s.options.SocketPath, "STOPPING=1\nSTATUS=GoDex is stopping")
}

func (s *NotifyService) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	interval := s.options.WatchdogInterval
	if interval <= 0 {
		<-ctx.Done()
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := sendNotify(s.options.SocketPath, "WATCHDOG=1\nSTATUS=GoDex watchdog heartbeat"); err != nil {
				return
			}
		}
	}
}

func sendNotify(socketPath, state string) error {
	socketPath = normalizeNotifySocket(socketPath)
	if socketPath == "" {
		return nil
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		return fmt.Errorf("connect notify socket: %w", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(state)); err != nil {
		return fmt.Errorf("write notify socket: %w", err)
	}
	return nil
}

func parseWatchdogInterval(raw string) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	micros, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || micros <= 0 {
		return 0
	}
	interval := time.Duration(micros) * time.Microsecond / 2
	if interval <= 0 {
		return time.Millisecond
	}
	return interval
}

func normalizeNotifySocket(socketPath string) string {
	socketPath = strings.TrimSpace(socketPath)
	if strings.HasPrefix(socketPath, "@") {
		return "\x00" + strings.TrimPrefix(socketPath, "@")
	}
	return socketPath
}
