package backend

import (
	"github.com/tim5wang/godex/internal/core/nodelib"
)

// Node library service methods (P3): thin wrappers over the core nodelib
// Manager so the HTTP API and future consumers share one access path.

func (s *Service) nodelibManager() *nodelib.Manager {
	return nodelib.NewManager(s.cfg.StateDir)
}

// ListNodeLibrary returns all node-library entries (builtin + user).
func (s *Service) ListNodeLibrary() ([]nodelib.Entry, error) {
	return s.nodelibManager().List()
}

// GetNodeLibraryEntry resolves one entry by id.
func (s *Service) GetNodeLibraryEntry(id string) (nodelib.Entry, error) {
	return s.nodelibManager().Get(id)
}

// SaveNodeLibraryEntry creates or updates a user entry.
func (s *Service) SaveNodeLibraryEntry(e nodelib.Entry) error {
	return s.nodelibManager().Save(e)
}

// DeleteNodeLibraryEntry removes a user entry.
func (s *Service) DeleteNodeLibraryEntry(id string) error {
	return s.nodelibManager().Delete(id)
}
