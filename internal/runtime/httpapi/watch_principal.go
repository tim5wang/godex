package httpapi

import (
	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/watchauth"
)

// watchDevicePrincipal is the identity of an authenticated paired watch (P0 §7,
// the last un-wired family).
//
// Watch routes authenticated the device with a scope check but attached no
// Principal, so every identity-based decision on them saw nobody. The scope
// itself stays the authorization mechanism (fixed per route, so a wrist cannot
// widen its own authority by asking) — this adds the missing half.
//
// KindWatch and a device-id subject make the audit trail name the wrist instead
// of leaving the caller anonymous. The device id is not a human identity: a
// paired watch is a device credential.
//
// No entitlements: a watch must not inherit operator capabilities, so a future
// gate requiring one denies it by default rather than letting it through.
//
// TenantID is empty because watchauth.Device has no tenant binding yet. That is
// the fail-safe reading, not a hole: Policy.Authorize treats an unscoped
// non-operator principal as cross-tenant, so a watch cannot reach tenant-scoped
// nodes by omission. Binding the device to its tenant is a P1 task; until then
// the gap fails closed.
func watchDevicePrincipal(device watchauth.Device) identity.Principal {
	return identity.Principal{
		UserID:     device.DeviceID,
		Kind:       identity.KindWatch,
		AuthMethod: AuthMethodWatch,
	}
}

// watchRelayPrincipal is the identity for a watch call that arrived as a relay
// forward from the node the wrist is paired with (withWatchScopeOrRelay).
//
// The forward proves the origin node vouched for a scope, but it carries scopes
// rather than the device id, so the wrist cannot be named on this path. Recording
// it as a relay-vouched watch call keeps the audit trail honest instead of
// inventing a device id.
//
// Still zero entitlements: a forward grants the scope it was asked for and
// nothing else, which is precisely the property withWatchScopeOrRelay exists to
// enforce — the principal must not undo it by handing out capabilities.
func watchRelayPrincipal() identity.Principal {
	return identity.Principal{
		UserID:     "watch-relay",
		Kind:       identity.KindWatch,
		AuthMethod: AuthMethodRelayTrust,
	}
}
