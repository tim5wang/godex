package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/tim5wang/godex/internal/platform/idgen"
	"github.com/tim5wang/godex/internal/platform/logger"
)

// ForwardSpec describes one managed TCP forward tunnel running inside the
// center process: the center listens on 127.0.0.1:LocalPort and relays every
// accepted connection over the node's relay channel to Target on the node's
// network. This is the ssh -L style jump-host behavior, but managed by the
// center itself (no external `godex node forward` process to babysit).
type ForwardSpec struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	NodeID    string `json:"node_id"`
	LocalPort int    `json:"local_port"`
	Target    string `json:"target"`
}

// ForwardState is the runtime state of one forward tunnel.
type ForwardState string

const (
	ForwardStateRunning ForwardState = "running"
	ForwardStateError   ForwardState = "error"
	ForwardStateStopped ForwardState = "stopped"
)

// ForwardStatus is the observable state of one forward tunnel.
type ForwardStatus struct {
	ForwardSpec
	State         ForwardState `json:"state"`
	Error         string       `json:"error,omitempty"`
	ActiveConns   int          `json:"active_conns"`
	LastCheckedAt time.Time    `json:"last_checked_at,omitempty"`
	LastLatencyMs int64        `json:"last_latency_ms,omitempty"`
}

// ForwardCheckStep is one leg of the end-to-end connectivity check for a
// forward tunnel: local listener, node relay link, and upstream target.
type ForwardCheckStep struct {
	Name      string `json:"name"`
	OK        bool   `json:"ok"`
	Detail    string `json:"detail"`
	LatencyMs int64  `json:"latency_ms,omitempty"`
}

// ForwardCheckResult aggregates the per-leg results.
type ForwardCheckResult struct {
	OK    bool               `json:"ok"`
	Steps []ForwardCheckStep `json:"steps"`
}

type forwardEntry struct {
	spec ForwardSpec

	mu      sync.Mutex
	ln      net.Listener
	state   ForwardState
	errMsg  string
	conns   map[net.Conn]struct{}
	checked time.Time
	latency int64
}

// ForwardServer manages zero or more TCP forward tunnels inside the center.
// It owns the listeners and the per-connection relay streams; the caller owns
// persistence (forward specs are read from config at startup and written back
// through the config manager when the REST API mutates them).
type ForwardServer struct {
	hub *Hub

	mu      sync.Mutex
	entries map[string]*forwardEntry // by spec.ID
}

// NewForwardServer creates an empty forward server bound to the relay hub.
func NewForwardServer(hub *Hub) *ForwardServer {
	if hub == nil {
		return nil
	}
	return &ForwardServer{hub: hub, entries: map[string]*forwardEntry{}}
}

// Start satisfies the app.LifecycleService interface. All tunnels are started
// eagerly by Add (the config loader calls Add before serve begins), so there
// is nothing to do here.
func (s *ForwardServer) Start(context.Context) error { return nil }

// Stop satisfies the app.LifecycleService interface.
func (s *ForwardServer) Stop(context.Context) error {
	s.Shutdown()
	return nil
}

// Add validates and starts one forward tunnel. ID may be empty (generated)
// or supplied by the caller; if a tunnel with the same ID already exists it
// is replaced (old listener closed) unless it is running and identical.
func (s *ForwardServer) Add(spec ForwardSpec) (ForwardSpec, error) {
	if err := validateForwardSpec(spec); err != nil {
		return spec, err
	}
	if spec.ID == "" {
		spec.ID = idgen.New("fw-", 4)
	}
	entry := &forwardEntry{spec: spec, conns: map[net.Conn]struct{}{}}

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.entries[spec.ID]; ok && existing.spec == spec {
		return spec, nil
	}
	if existing, ok := s.entries[spec.ID]; ok {
		existing.stop()
	}
	if err := entry.start(s.hub); err != nil {
		entry.state = ForwardStateError
		entry.errMsg = err.Error()
		s.entries[spec.ID] = entry
		return spec, fmt.Errorf("start forward on 127.0.0.1:%d: %w", spec.LocalPort, err)
	}
	s.entries[spec.ID] = entry
	return spec, nil
}

// Get returns the status of one tunnel.
func (s *ForwardServer) Get(id string) (ForwardStatus, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	if !ok {
		return ForwardStatus{}, false
	}
	return entry.status(), true
}

// List returns the status of every managed tunnel, sorted by local port.
func (s *ForwardServer) List() []ForwardStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ForwardStatus, 0, len(s.entries))
	for _, entry := range s.entries {
		out = append(out, entry.status())
	}
	// Stable order by local port then id.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && lessForwardStatus(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Remove stops and forgets one tunnel.
func (s *ForwardServer) Remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	if !ok {
		return false
	}
	entry.stop()
	delete(s.entries, id)
	return true
}

// Check probes every leg of one tunnel end to end: the local listener, the
// node's relay link, and the upstream target (by dialing it through the
// relay). It records the last-check timestamp and latency on the tunnel.
func (s *ForwardServer) Check(id string) (ForwardCheckResult, error) {
	s.mu.Lock()
	entry, ok := s.entries[id]
	s.mu.Unlock()
	if !ok {
		return ForwardCheckResult{}, fmt.Errorf("forward %q not found", id)
	}

	result := ForwardCheckResult{Steps: []ForwardCheckStep{}}
	record := func(name string, ok bool, detail string, latencyMs int64) {
		result.Steps = append(result.Steps, ForwardCheckStep{
			Name: name, OK: ok, Detail: detail, LatencyMs: latencyMs,
		})
		if !ok {
			result.OK = false
		}
	}
	result.OK = true

	// Leg 1: the local listener is owned by this process; a listener exists
	// unless the tunnel stopped or failed to start.
	entry.mu.Lock()
	state := entry.state
	errMsg := entry.errMsg
	hasListener := entry.ln != nil
	entry.mu.Unlock()
	if hasListener && state == ForwardStateRunning {
		record("listener", true, fmt.Sprintf("127.0.0.1:%d listening", entry.spec.LocalPort), 0)
	} else {
		reason := errMsg
		if reason == "" {
			reason = "not listening"
		}
		record("listener", false, reason, 0)
		return result, nil
	}

	// Leg 2: the node must have a live relay connection.
	if !s.hub.IsOnline(entry.spec.NodeID) {
		record("node", false, fmt.Sprintf("node %q relay offline", entry.spec.NodeID), 0)
		return result, nil
	}
	record("node", true, fmt.Sprintf("node %q relay connected", entry.spec.NodeID), 0)

	// Leg 3: dial the target through the relay; close the probe immediately.
	start := time.Now()
	probeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stream, err := s.hub.OpenTCPStream(probeCtx, entry.spec.NodeID, idgen.New("fw-", 4), entry.spec.Target)
	dialMs := time.Since(start).Milliseconds()
	if err != nil {
		record("target", false, fmt.Sprintf("dial %s: %v", entry.spec.Target, err), dialMs)
		return result, nil
	}
	_ = stream.Close()
	record("target", true, fmt.Sprintf("dial %s ok", entry.spec.Target), dialMs)

	entry.mu.Lock()
	entry.checked = time.Now()
	entry.latency = dialMs
	entry.mu.Unlock()
	return result, nil
}

// Shutdown stops every tunnel (used by the serve lifecycle).
func (s *ForwardServer) Shutdown() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.entries {
		entry.stop()
	}
}

func validateForwardSpec(spec ForwardSpec) error {
	if strings.TrimSpace(spec.NodeID) == "" {
		return errors.New("forward requires a node id")
	}
	if spec.LocalPort <= 0 || spec.LocalPort > 65535 {
		return fmt.Errorf("invalid local port %d", spec.LocalPort)
	}
	if strings.TrimSpace(spec.Target) == "" {
		return errors.New("forward requires a target host:port")
	}
	return nil
}

func (e *forwardEntry) start(hub *Hub) error {
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", e.spec.LocalPort))
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.ln = ln
	e.state = ForwardStateRunning
	e.errMsg = ""
	e.mu.Unlock()
	go e.acceptLoop(hub, ln)
	return nil
}

func (e *forwardEntry) acceptLoop(hub *Hub, ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			e.mu.Lock()
			stopped := e.ln != ln
			e.mu.Unlock()
			if stopped {
				return
			}
			// Listener failed unexpectedly; mark the tunnel errored.
			e.mu.Lock()
			e.state = ForwardStateError
			e.errMsg = err.Error()
			e.mu.Unlock()
			logger.Errorf("forward %s accept: %v", e.spec.ID, err)
			return
		}
		e.mu.Lock()
		e.conns[conn] = struct{}{}
		e.mu.Unlock()
		go e.bridge(hub, conn)
	}
}

// bridge relays one accepted local connection to the node-side target via the
// hub's TCP stream channel, copying bytes in both directions until either
// side closes.
func (e *forwardEntry) bridge(hub *Hub, local net.Conn) {
	defer func() {
		_ = local.Close()
		e.mu.Lock()
		delete(e.conns, local)
		e.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stream, err := hub.OpenTCPStream(ctx, e.spec.NodeID, idgen.New("fw-", 4), e.spec.Target)
	if err != nil {
		// Node offline or target unreachable: surface once per connection by
		// closing the local side immediately.
		return
	}
	defer stream.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stream, local)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(local, stream)
	}()
	wg.Wait()
}

func (e *forwardEntry) stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ln != nil {
		ln := e.ln
		e.ln = nil
		_ = ln.Close()
	}
	for conn := range e.conns {
		_ = conn.Close()
	}
	e.conns = map[net.Conn]struct{}{}
	e.state = ForwardStateStopped
	e.errMsg = ""
}

func (e *forwardEntry) status() ForwardStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	return ForwardStatus{
		ForwardSpec:   e.spec,
		State:         e.state,
		Error:         e.errMsg,
		ActiveConns:   len(e.conns),
		LastCheckedAt: e.checked,
		LastLatencyMs: e.latency,
	}
}

func lessForwardStatus(a, b ForwardStatus) bool {
	if a.LocalPort != b.LocalPort {
		return a.LocalPort < b.LocalPort
	}
	return a.ID < b.ID
}
