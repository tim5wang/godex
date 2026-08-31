package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/tim5wang/godex/internal/contracts/protocol"
)

// ErrTranscriptNotFound marks a transcript archive ref that is unknown to the
// session (or missing on disk). The HTTP layer maps it to 404.
var ErrTranscriptNotFound = errors.New("transcript archive not found")

// ReadTranscript returns the archived pre-compaction messages for a transcript
// ref belonging to the given session. Ref must be a bare archive filename in
// the configured transcripts dir (path traversal is rejected), and it must be
// referenced by the session's own transcript refs so one session cannot read
// another session's archives through this endpoint.
func (s *Service) ReadTranscript(sessionID, ref string) ([]protocol.Message, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || filepath.Base(ref) != ref || ref == "." {
		return nil, fmt.Errorf("%w: invalid ref", ErrTranscriptNotFound)
	}
	if strings.TrimSpace(s.cfg.TranscriptsDir) == "" {
		return nil, fmt.Errorf("%w: transcripts dir not configured", ErrTranscriptNotFound)
	}

	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	refs := uniqueTranscriptRefs(session.agent.TranscriptRefs())
	if !slices.Contains(refs, ref) {
		return nil, fmt.Errorf("%w: %s", ErrTranscriptNotFound, ref)
	}

	data, err := os.ReadFile(filepath.Join(s.cfg.TranscriptsDir, ref))
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrTranscriptNotFound, ref)
	}
	var messages []protocol.Message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, fmt.Errorf("parse transcript %s: %w", ref, err)
	}
	return messages, nil
}
