package httpapi

import (
	"strings"

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
// TenantID comes from the device record, which gained its ownership when the pairing
// was approved. It is what makes the wrist reach exactly the nodes its owner may reach
// and no others: Policy.Authorize treats a principal with the right tenant as in-tenant
// and anyone else as cross-tenant. A device paired before ownership existed still has
// an empty TenantID, which stays the fail-safe reading — an unscoped non-operator is
// refused rather than silently admitted.
func watchDevicePrincipal(device watchauth.Device) identity.Principal {
	return identity.Principal{
		TenantID:   strings.TrimSpace(device.TenantID),
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
