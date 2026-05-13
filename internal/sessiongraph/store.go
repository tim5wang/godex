package sessiongraph

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/tim5wang/godex/internal/platform/fsutil"
)

type Store struct {
	path string
}

func NewStore(path string) Store {
	return Store{path: path}
}

func (s Store) Load() (*SessionGraph, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &SessionGraph{}, nil
		}
		return nil, err
	}
	var graph SessionGraph
	if err := json.Unmarshal(data, &graph); err != nil {
		return nil, err
	}
	return &graph, nil
}

func (s Store) Save(graph *SessionGraph) error {
	if graph == nil {
		graph = &SessionGraph{}
	}
	return fsutil.WriteJSONAtomic(s.path, graph, 0644)
}
