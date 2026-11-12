package httpapi

import (
	"context"
	"github.com/tim5wang/godex/internal/core/memory"
	"github.com/tim5wang/godex/internal/services/watchauth"
	"testing"
)

func TestWatchMemoryOwnerUsesApproverInsteadOfAuditDevice(t *testing.T) {
	device := watchauth.Device{DeviceID: "wrist", TenantID: "tenant", OwnerUserID: "alice"}
	ctx := withWatchMemoryOwner(WithPrincipal(context.Background(), watchDevicePrincipal(device)), device)
	principal, _ := PrincipalFromContext(ctx)
	owner, ok := memory.OwnerFromContext(ctx)
	if !ok || owner.TenantID != "tenant" || owner.UserID != "alice" || principal.UserID != "wrist" {
		t.Fatalf("owner/audit identity confused: %+v %+v", owner, principal)
	}
	legacy, _ := memory.OwnerFromContext(withWatchMemoryOwner(context.Background(), watchauth.Device{DeviceID: "legacy"}))
	if legacy.UserID != "local-operator" {
		t.Fatal("legacy local pairing lost compatibility")
	}
	unassigned, _ := memory.OwnerFromContext(withWatchMemoryOwner(context.Background(), watchauth.Device{DeviceID: "unknown", TenantID: "tenant"}))
	if unassigned.UserID == "local-operator" {
		t.Fatal("unowned tenant device inherited operator memory")
	}
}
