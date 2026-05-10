package noderegistry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

type RemoteHeartbeat struct {
	centerURL string
	token     string
	node      NodeInput
	interval  time.Duration
	client    *http.Client

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

type LocalHeartbeat struct {
	registry *Registry
	node     NodeInput
	interval time.Duration

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func NewLocalHeartbeat(registry *Registry, node NodeInput, interval time.Duration) *LocalHeartbeat {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &LocalHeartbeat{
		registry: registry,
		node:     node,
		interval: interval,
	}
}

func (h *LocalHeartbeat) Start(ctx context.Context) error {
	if h == nil || h.registry == nil || h.node.ID == "" {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		_, _ = h.registry.Heartbeat(runCtx, h.node.ID, h.node)
		ticker := time.NewTicker(h.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				_, _ = h.registry.Heartbeat(runCtx, h.node.ID, h.node)
			}
		}
	}()
	return nil
}

func (h *LocalHeartbeat) Stop(ctx context.Context) error {
	if h == nil || h.cancel == nil {
		return nil
	}
	h.cancel()
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func NewRemoteHeartbeat(centerURL, token string, node NodeInput, interval time.Duration) *RemoteHeartbeat {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &RemoteHeartbeat{
		centerURL: strings.TrimRight(strings.TrimSpace(centerURL), "/"),
		token:     strings.TrimSpace(token),
		node:      node,
		interval:  interval,
		client:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (h *RemoteHeartbeat) Start(ctx context.Context) error {
	if h == nil || h.centerURL == "" || h.node.ID == "" {
		return nil
	}
	runCtx, cancel := context.WithCancel(ctx)
	h.cancel = cancel
	h.wg.Add(1)
	go func() {
		defer h.wg.Done()
		_ = h.post(runCtx, "POST", "/control/nodes/register", h.node)
		ticker := time.NewTicker(h.interval)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				_ = h.post(runCtx, "POST", "/control/nodes/"+h.node.ID+"/heartbeat", h.node)
			}
		}
	}()
	return nil
}

func (h *RemoteHeartbeat) Stop(ctx context.Context) error {
	if h == nil || h.cancel == nil {
		return nil
	}
	h.cancel()
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

func (h *RemoteHeartbeat) post(ctx context.Context, method, path string, body any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, h.apiURL(path), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("node heartbeat failed: %s", resp.Status)
	}
	return nil
}

func (h *RemoteHeartbeat) apiURL(path string) string {
	base := strings.TrimRight(h.centerURL, "/")
	if strings.HasSuffix(base, "/api") {
		return base + path
	}
	return base + "/api" + path
}
