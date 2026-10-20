package backend

import (
	"context"
	"encoding/json"
	"path/filepath"

	"github.com/tim5wang/godex/internal/domain/events"
	"github.com/tim5wang/godex/internal/sessiongraph"
)

type sessionOpenSnapshot struct {
	sessionCheckpointSnapshot
	Graph          *sessiongraph.SessionGraph
	QueueRecovered bool
}

// readSessionForOpen keeps the checkpoint and store snapshot local to one
// load. It must not be a long-lived cache: new opens need fresh durable state.
func (s *Service) readSessionForOpen(id string) (*sessionOpenSnapshot, error) {
	data, stored, err := s.readSessionStoreData(context.Background(), id)
	if err != nil {
		return nil, err
	}
	var cp *sessionCheckpointSnapshot
	var cpErr error
	if stored {
		if data.Checkpoint != nil && len(data.Checkpoint.Manifest) > 0 && len(data.Checkpoint.State) > 0 {
			cp, _, cpErr = decodeStoredSessionCheckpoint(id, data.Checkpoint)
		}
	} else {
		cp, _, cpErr = s.readFileSessionCheckpoint(id)
	}
	if cpErr != nil {
		cp = nil
	}
	out := &sessionOpenSnapshot{}
	if stored {
		out.Manifest, out.State, err = s.decodeStoredSessionFiles(id, data)
	} else {
		out.Manifest, out.State, err = s.readSessionFilesWithCheckpoint(id, cp, cpErr)
	}
	if err != nil {
		return nil, err
	}
	if cp != nil {
		out.Timeline, out.Turns, out.Queue = cp.Timeline, cp.Turns, cp.Queue
	}
	readRoot := func(name string, target any) {
		if raw, ok, err := readOptionalFile(filepath.Join(s.sessionDir(id), name)); err == nil && ok {
			_ = json.Unmarshal(raw, target)
		}
	}
	if stored && len(data.Timeline) > 0 && json.Unmarshal(data.Timeline, &out.Timeline) == nil {
		// The store's root snapshot is authoritative.
	} else if cp == nil || s.legacyFileNewerThanCheckpoint(id, timelineFileName) {
		out.Timeline = s.readRootSessionTimeline(id)
	}
	if stored && len(data.Turns) > 0 {
		_ = json.Unmarshal(data.Turns, &out.Turns)
	} else if cp == nil || s.legacyFileNewerThanCheckpoint(id, turnsFileName) {
		readRoot(turnsFileName, &out.Turns)
	}
	if stored && len(data.Queue) > 0 {
		_ = json.Unmarshal(data.Queue, &out.Queue)
	} else if cp == nil || s.legacyFileNewerThanCheckpoint(id, turnQueueFileName) {
		readRoot(turnQueueFileName, &out.Queue)
	}
	out.Turns = normalizeTurnRecords(out.Turns)
	out.Queue = normalizeQueuedTurns(out.Queue)
	if s.cfg != nil && s.cfg.Runtime.Recovery.AutoRepairSessions {
		for i := range out.Queue {
			if out.Queue[i].Status == "injected" {
				out.Queue[i].Status = "queued"
				out.Queue[i].Mode = QueueModeFollowUp
				out.Queue[i].UpdatedAt = s.now()
				if out.Queue[i].Envelope.Metadata == nil {
					out.Queue[i].Envelope.Metadata = make(map[string]string)
				}
				out.Queue[i].Envelope.Metadata["queue_mode"] = string(QueueModeFollowUp)
				out.QueueRecovered = true
			}
		}
	}
	var journal []events.Event
	if stored && len(data.EventJournal) > 0 {
		journal = decodeSessionEventJournal(data.EventJournal)
	} else {
		// Avoid calling readSessionEventJournal, which would Load the entire
		// SQLite row again just to find an empty journal.
		if raw, ok, err := readOptionalFile(filepath.Join(s.sessionDir(id), eventJournalFileName)); err == nil && ok {
			journal = decodeSessionEventJournal(raw)
		}
	}
	out.Timeline = mergeTimelineEvents(out.Timeline, journal)
	if stored && len(data.Graph) > 0 {
		out.Graph = &sessiongraph.SessionGraph{}
		if err := json.Unmarshal(data.Graph, out.Graph); err != nil {
			return nil, err
		}
		out.Graph.EnsureMainBranch()
	} else {
		graph, _ := sessiongraph.NewStore(s.sessionGraphPath(id)).Load()
		if graph == nil {
			graph = &sessiongraph.SessionGraph{}
		}
		graph.EnsureMainBranch()
		out.Graph = graph
	}
	return out, nil
}
