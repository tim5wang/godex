package relay

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/tim5wang/godex/internal/core/identity"
)

// startForwardCenterWithAudit is startForwardCenter plus a NodeAccess policy and
// an audit recorder, so both the admission decision AND the per-target decision
// can be observed.
func startForwardCenterWithAudit(
	t *testing.T,
	authorize func(*http.Request, string) identity.Decision,
	recorder func(ForwardAudit),
) (*httptest.Server, *testNodeClient, *Hub) {
	t.Helper()
	hub, hubServer := newTestHub(func(nodeID, credential string) bool { return true })
	t.Cleanup(func() { hubServer.Close(); _ = hub.Shutdown(context.Background()) })

	node := dialTestNode(t, "ws"+strings.TrimPrefix(hubServer.URL, "http"))
	t.Cleanup(node.close)
	node.send(t, Frame{Type: FrameHello, NodeID: "node-a", Credential: "x", Version: "v1.2.0"})
	if ok := node.recv(t); ok.Type != FrameHelloOK {
		t.Fatalf("expected hello_ok, got %#v", ok)
	}

	handler := NewForwardHandler(hub, nil)
	handler.AuthorizeNode = authorize
	handler.AuditRecorder = recorder
	forwardServer := httptest.NewServer(handler)
	t.Cleanup(forwardServer.Close)
	return forwardServer, node, hub
}

// auditSink collects recorded attempts behind a mutex, because the refusal path
// records from the request goroutine while a success records from the session
// goroutine.
type auditSink struct {
	mu       sync.Mutex
	attempts []ForwardAudit
}

func (s *auditSink) record(attempt ForwardAudit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, attempt)
}

func (s *auditSink) snapshot() []ForwardAudit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ForwardAudit(nil), s.attempts...)
}

// waitForAttempts polls until the sink holds want rows.
//
// Needed because the attempt is recorded on the SESSION goroutine (handleOpen),
// not on the caller's: ForwardClient.Open is asynchronous by design — it returns
// a stream immediately and the dial result (ack, or a refusal) arrives later as a
// frame — so asserting right after Open would be a race, and a flaky one.
func waitForAttempts(t *testing.T, sink *auditSink, want int) []ForwardAudit {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if attempts := sink.snapshot(); len(attempts) >= want {
			return attempts
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("recorded %d attempts within the deadline, want %d", len(sink.snapshot()), want)
	return nil
}

// TestForwardNodeLevelDenialIsAudited is the case the audit trail exists for.
//
// Before this, a refusal was invisible: the caller got 403 and nothing anywhere
// recorded that an attempt had happened at all — no row, no log on the center
// side beyond a WARN that named no target, and nothing at all on the node side.
// An audit trail that records only successes cannot answer "did anyone try to
// reach a service they may not touch?", which is its whole purpose.
func TestForwardNodeLevelDenialIsAudited(t *testing.T) {
	sink := &auditSink{}
	server, _, _ := startForwardCenterWithAudit(t,
		func(*http.Request, string) identity.Decision {
			decision := identity.Denied(identity.ReasonCrossTenant)
			decision.TenantID = "tenant-a"
			decision.UserID = "user-a"
			return decision
		},
		sink.record,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, _, err := websocket.DefaultDialer.DialContext(ctx, forwardURLFor(server, "node-a"), nil); err == nil {
		t.Fatal("expected the upgrade to fail for a denied caller")
	}

	attempts := sink.snapshot()
	if len(attempts) != 1 {
		t.Fatalf("recorded %d attempts, want 1: %#v", len(attempts), attempts)
	}
	got := attempts[0]
	if got.Allowed {
		t.Errorf("denied admission was audited as allowed: %#v", got)
	}
	if got.NodeID != "node-a" {
		t.Errorf("node = %q, want node-a", got.NodeID)
	}
	// The identity must come from the DECISION, not from request headers: a
	// caller must not be able to choose whose audit trail its attempt lands in.
	if got.TenantID != "tenant-a" || got.UserID != "user-a" {
		t.Errorf("identity = (%q,%q), want (tenant-a,user-a)", got.TenantID, got.UserID)
	}
	// No target was named: the whole node was refused, and saying so is more
	// truthful than reporting an empty string as if a target had been chosen.
	if got.Target != "" {
		t.Errorf("target = %q, want empty for a node-level refusal", got.Target)
	}
	if !strings.Contains(got.Reason, identity.ReasonCrossTenant) {
		t.Errorf("reason = %q, want it to carry %q", got.Reason, identity.ReasonCrossTenant)
	}
}

// TestForwardTargetRefusalIsAudited covers the node's own egress refusal: the
// center admitted the caller, the target was rejected further in (the node
// answers tcp_close "forward target not allowed", which OpenTCPStream surfaces
// as an error). This is the per-tenant egress boundary, and an attempt against
// it must be recorded as a refusal rather than as traffic.
func TestForwardTargetRefusalIsAudited(t *testing.T) {
	sink := &auditSink{}
	server, node, _ := startForwardCenterWithAudit(t,
		func(*http.Request, string) identity.Decision {
			return identity.Decision{Allowed: true, TenantID: "tenant-a", UserID: "user-a"}
		},
		sink.record,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialForward(ctx, forwardURLFor(server, "node-a"), "")
	if err != nil {
		t.Fatalf("DialForward: %v", err)
	}
	defer client.Close()

	// The node refuses the target instead of acking the dial.
	refused := make(chan struct{})
	go func() {
		frame, err := node.recvErr()
		if err != nil {
			t.Errorf("read tcp_open: %v", err)
			close(refused)
			return
		}
		if frame.Type != FrameTCPOpen {
			t.Errorf("expected tcp_open, got %#v", frame)
		}
		op := decodeTCPOpen(t, frame)
		node.sendErr(Frame{Type: FrameTCPClose, Payload: tcpClosePayloadJSON(op.ConnID, "forward target not allowed")})
		close(refused)
	}()

	// Open returns a stream immediately (the protocol is asynchronous); the
	// refusal surfaces as a close delivered on that stream, which is how a real
	// CLI client learns its target was refused.
	stream, err := client.Open("169.254.169.254:80")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer stream.Close()
	// A single Read must report the close. Asserting on io.ReadAll would be wrong:
	// ReadAll treats io.EOF as normal completion and returns a nil error, so it
	// cannot distinguish "the stream ended because the target was refused" from
	// "the stream ended cleanly".
	buf := make([]byte, 1)
	if _, err := stream.Read(buf); err == nil {
		t.Fatal("expected the refused target to close the stream")
	}
	<-refused

	attempts := waitForAttempts(t, sink, 1)
	got := attempts[0]
	if got.Allowed {
		t.Errorf("refused target was audited as allowed: %#v", got)
	}
	if got.Target != "169.254.169.254:80" {
		t.Errorf("target = %q, want the requested host:port", got.Target)
	}
	if !strings.Contains(got.Reason, "not allowed") {
		t.Errorf("reason = %q, want the node's refusal to survive into the audit", got.Reason)
	}
	if got.TenantID != "tenant-a" {
		t.Errorf("tenant = %q, want tenant-a", got.TenantID)
	}
}

// TestForwardAllowedAttemptIsAudited is the "not only refusals" half: a successful
// open is recorded as allowed, so the trail shows traffic AND attempts against
// the boundary rather than replacing one with the other.
func TestForwardAllowedAttemptIsAudited(t *testing.T) {
	sink := &auditSink{}
	server, node, _ := startForwardCenterWithAudit(t,
		func(*http.Request, string) identity.Decision {
			return identity.Decision{Allowed: true, TenantID: "tenant-a", UserID: "user-a"}
		},
		sink.record,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := DialForward(ctx, forwardURLFor(server, "node-a"), "")
	if err != nil {
		t.Fatalf("DialForward: %v", err)
	}
	defer client.Close()

	ackDone := make(chan struct{})
	go func() {
		expectOpenAndAck(t, node, "", "127.0.0.1:3306")
		close(ackDone)
	}()
	stream, err := client.Open("127.0.0.1:3306")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer stream.Close()
	<-ackDone

	// Recorded on the session goroutine once the ack is processed, so wait for it
	// rather than assuming the two goroutines have already met.
	attempts := waitForAttempts(t, sink, 1)
	got := attempts[0]
	if !got.Allowed {
		t.Errorf("allowed attempt was audited as refused: %#v", got)
	}
	if got.Target != "127.0.0.1:3306" {
		t.Errorf("target = %q, want 127.0.0.1:3306", got.Target)
	}
	if got.Reason != "" {
		t.Errorf("allowed attempt carries a reason %q, want empty", got.Reason)
	}
}

// TestForwardAuditRecorderAbsentIsUnchanged pins the compatibility contract: no
// recorder wired means the forward path behaves exactly as before, so an
// instance that never configures auditing cannot start failing because of it.
func TestForwardAuditRecorderAbsentIsUnchanged(t *testing.T) {
	server, _, _ := startForwardCenterWithAudit(t,
		func(*http.Request, string) identity.Decision {
			decision := identity.Denied(identity.ReasonCrossTenant)
			return decision
		},
		nil,
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, resp, err := websocket.DefaultDialer.DialContext(ctx, forwardURLFor(server, "node-a"), nil)
	if err == nil {
		t.Fatal("expected the upgrade to fail for a denied caller")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %v, want 403", resp)
	}
}
