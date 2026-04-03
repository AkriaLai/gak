// Package session implements conversation persistence and checkpoint/resume.
//
// 断点续传 (Checkpoint/Resume):
//   - Save: Serialize the full AgentState to disk after each turn
//   - Resume: Load a previous checkpoint and continue from that point
//   - This is made trivial by Principle 5 (Immutable State) —
//     each state is a self-contained snapshot that can be serialized independently
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/akria/gak/pkg/state"
)

// Checkpoint represents a saved state at a specific point in time.
type Checkpoint struct {
	// ID uniquely identifies this checkpoint.
	ID string `json:"id"`

	// Timestamp records when the checkpoint was created.
	Timestamp time.Time `json:"timestamp"`

	// Turn is the inference turn number at checkpoint time.
	Turn int `json:"turn"`

	// State is the full agent state snapshot.
	State state.AgentState `json:"state"`

	// Metadata holds optional checkpoint metadata.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Manager handles session persistence and checkpoint management.
type Manager struct {
	// sessionDir is the directory for this session's checkpoints.
	sessionDir string

	// sessionID uniquely identifies the current session.
	sessionID string

	// autoSave controls whether checkpoints are created automatically.
	autoSave bool

	// maxCheckpoints limits the number of retained checkpoints.
	maxCheckpoints int
}

// NewManager creates a new session manager.
func NewManager(baseDir, sessionID string) (*Manager, error) {
	sessionDir := filepath.Join(baseDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		return nil, fmt.Errorf("creating session dir: %w", err)
	}

	return &Manager{
		sessionDir:     sessionDir,
		sessionID:      sessionID,
		autoSave:       true,
		maxCheckpoints: 50,
	}, nil
}

// SetAutoSave enables/disables automatic checkpointing.
func (m *Manager) SetAutoSave(enabled bool) {
	m.autoSave = enabled
}

// ShouldAutoSave returns whether auto-save is enabled.
func (m *Manager) ShouldAutoSave() bool {
	return m.autoSave
}

// Save creates a checkpoint from the current state.
func (m *Manager) Save(agentState state.AgentState, metadata map[string]string) (*Checkpoint, error) {
	cp := &Checkpoint{
		ID:        fmt.Sprintf("cp_%d_%d", agentState.Turn, time.Now().UnixMilli()),
		Timestamp: time.Now(),
		Turn:      agentState.Turn,
		State:     agentState,
		Metadata:  metadata,
	}

	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling checkpoint: %w", err)
	}

	path := filepath.Join(m.sessionDir, cp.ID+".json")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return nil, fmt.Errorf("writing checkpoint: %w", err)
	}

	// Prune old checkpoints
	m.prune()

	return cp, nil
}

// Load reads a specific checkpoint by ID.
func (m *Manager) Load(checkpointID string) (*Checkpoint, error) {
	path := filepath.Join(m.sessionDir, checkpointID+".json")
	return loadCheckpointFile(path)
}

// Latest returns the most recent checkpoint, or nil if none exist.
func (m *Manager) Latest() (*Checkpoint, error) {
	checkpoints, err := m.List()
	if err != nil {
		return nil, err
	}
	if len(checkpoints) == 0 {
		return nil, nil
	}
	return m.Load(checkpoints[len(checkpoints)-1].ID)
}

// List returns all checkpoints for this session, ordered by time.
func (m *Manager) List() ([]Checkpoint, error) {
	entries, err := os.ReadDir(m.sessionDir)
	if err != nil {
		return nil, fmt.Errorf("reading session dir: %w", err)
	}

	var checkpoints []Checkpoint
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		path := filepath.Join(m.sessionDir, entry.Name())
		cp, err := loadCheckpointFile(path)
		if err != nil {
			continue
		}
		checkpoints = append(checkpoints, *cp)
	}

	sort.Slice(checkpoints, func(i, j int) bool {
		return checkpoints[i].Timestamp.Before(checkpoints[j].Timestamp)
	})

	return checkpoints, nil
}

// Resume loads the latest checkpoint and returns the state.
// Returns nil state if no checkpoints exist (fresh session).
func (m *Manager) Resume() (*state.AgentState, error) {
	cp, err := m.Latest()
	if err != nil {
		return nil, err
	}
	if cp == nil {
		return nil, nil
	}
	return &cp.State, nil
}

// Rollback restores state to a specific checkpoint.
// All checkpoints after the target are deleted.
func (m *Manager) Rollback(checkpointID string) (*state.AgentState, error) {
	target, err := m.Load(checkpointID)
	if err != nil {
		return nil, fmt.Errorf("loading target checkpoint: %w", err)
	}

	// Delete all checkpoints after the target
	checkpoints, err := m.List()
	if err != nil {
		return nil, err
	}

	for _, cp := range checkpoints {
		if cp.Timestamp.After(target.Timestamp) {
			path := filepath.Join(m.sessionDir, cp.ID+".json")
			os.Remove(path)
		}
	}

	return &target.State, nil
}

// SessionID returns the current session ID.
func (m *Manager) SessionID() string {
	return m.sessionID
}

// prune removes excess checkpoints beyond maxCheckpoints.
func (m *Manager) prune() {
	checkpoints, err := m.List()
	if err != nil {
		return
	}

	if len(checkpoints) <= m.maxCheckpoints {
		return
	}

	// Remove oldest checkpoints
	toRemove := len(checkpoints) - m.maxCheckpoints
	for i := 0; i < toRemove; i++ {
		path := filepath.Join(m.sessionDir, checkpoints[i].ID+".json")
		os.Remove(path)
	}
}

func loadCheckpointFile(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, err
	}

	return &cp, nil
}
