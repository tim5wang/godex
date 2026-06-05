package cron

import (
	"context"

	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/domain/message"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
)

// ModelProfileSetter is the slice of *rtbackend.Service that the cron runtime
// needs in order to pin a freshly opened session to a configured model
// profile. The real backend returns (ModelsView, error) so it can refresh the
// model picker UI; cron only needs the error to short-circuit the run.
type ModelProfileSetter interface {
	SetSessionModelProfile(ctx context.Context, sessionID, profileID string) (rtbackend.ModelsView, error)
}

// NewBackendAdapter returns a Backend implementation that wraps the real
// *rtbackend.Service. The adapter exists to bridge two minor contract gaps:
//   - SetSessionModelProfile returns (ModelsView, error); cron only needs
//     the error.
//   - All other methods are straight delegations, but the wrapping makes the
//     call site explicit about the type contract and gives us a place to add
//     cron-specific logging / metrics in the future.
func NewBackendAdapter(service *rtbackend.Service) Backend {
	return backendAdapter{service: service}
}

type backendAdapter struct {
	service *rtbackend.Service
}

func (a backendAdapter) OpenSession(ctx context.Context, locator rtbackend.SessionLocator) (*rtbackend.OpenedSession, error) {
	return a.service.OpenSession(ctx, locator)
}

func (a backendAdapter) Submit(ctx context.Context, sessionID string, envelope message.Envelope) (*rtbackend.SubmitResult, error) {
	return a.service.Submit(ctx, sessionID, envelope)
}

func (a backendAdapter) AttachSink(sessionID string, sink events.Sink) (func(), error) {
	return a.service.AttachSink(sessionID, sink)
}

func (a backendAdapter) SetSessionModelProfile(ctx context.Context, sessionID, profileID string) error {
	_, err := a.service.SetSessionModelProfile(ctx, sessionID, profileID)
	return err
}

// Compile-time assertion that *rtbackend.Service still implements the wider
// ModelProfileSetter shape used by the adapter. If the real backend ever
// drops or renames that method, this line will fail to build, surfacing the
// contract change here instead of at the call site.
var _ ModelProfileSetter = (*rtbackend.Service)(nil)
