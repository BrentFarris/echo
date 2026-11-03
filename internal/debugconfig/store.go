package debugconfig

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/brent/echo/internal/store"
)

type StateStore struct{ data *store.Store }

func NewStateStore(data *store.Store) *StateStore { return &StateStore{data: data} }
func (s *StateStore) Load(id string) (State, error) {
	var state State
	err := s.data.Get("editor-debug", id, &state)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return state, err
}
func (s *StateStore) Save(id string, expected uint64, state State) (State, error) {
	state = state.Normalized()
	if err := state.Validate(); err != nil {
		return State{}, err
	}
	raw, err := json.Marshal(state)
	if err != nil || len(raw) > 1<<20 {
		return State{}, fmt.Errorf("debug state exceeds the 1 MiB limit")
	}
	err = s.data.Update("editor-debug", id, func(raw json.RawMessage) (any, error) {
		var current State
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &current); err != nil {
				return nil, err
			}
		}
		if current.Revision != expected {
			return nil, &RevisionConflict{Expected: expected, Actual: current.Revision}
		}
		state.Revision = current.Revision + 1
		return state, nil
	})
	return state, err
}
