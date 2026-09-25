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
	if !validTranscriptRef(ref) {
		return nil, fmt.Errorf("%w: invalid ref", ErrTranscriptNotFound)
	}
	if strings.TrimSpace(s.cfg.TranscriptsDir) == "" {
		return nil, fmt.Errorf("%w: transcripts dir not configured", ErrTranscriptNotFound)
	}

	session, err := s.requireSession(sessionID)
	if err != nil {
		return nil, err
	}
	if !s.ownsTranscriptRef(session, ref) {
		return nil, fmt.Errorf("%w: %s", ErrTranscriptNotFound, ref)
	}

	messages, err := s.readTranscriptArchiveFile(ref)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrTranscriptNotFound, ref)
	}
	return messages, nil
}

func (s *Service) ownsTranscriptRef(session *sessionState, target string) bool {
	if session == nil || session.agent == nil {
		return false
	}
	roots := uniqueTranscriptRefs(session.agent.TranscriptRefs())
	if slices.Contains(roots, target) {
		return true
	}

	pending := append([]string{}, roots...)
	visited := make(map[string]struct{}, len(roots))
	for len(pending) > 0 {
		ref := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if !validTranscriptRef(ref) {
			continue
		}
		if _, seen := visited[ref]; seen {
			continue
		}
		visited[ref] = struct{}{}
		messages, err := s.readTranscriptArchiveFile(ref)
		if err != nil {
			continue
		}
		for _, msg := range messages {
			if msg.Metadata == nil || msg.Metadata.Kind != protocol.KindSummary {
				continue
			}
			olderRef := strings.TrimSpace(msg.Metadata.Transcript)
			if olderRef == target {
				return true
			}
			pending = append(pending, olderRef)
		}
	}
	return false
}

func (s *Service) readTranscriptArchiveFile(ref string) ([]protocol.Message, error) {
	if !validTranscriptRef(ref) {
		return nil, fmt.Errorf("invalid transcript ref")
	}
	data, err := os.ReadFile(filepath.Join(s.cfg.TranscriptsDir, ref))
	if err != nil {
		return nil, err
	}
	var messages []protocol.Message
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, fmt.Errorf("parse transcript %s: %w", ref, err)
	}
	return messages, nil
}

func validTranscriptRef(ref string) bool {
	return ref != "" &&
		ref != "." &&
		ref != ".." &&
		filepath.Base(ref) == ref &&
		!filepath.IsAbs(ref) &&
		!strings.ContainsAny(ref, `/\`)
}
