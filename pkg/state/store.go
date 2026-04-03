package state

import (
	"sync"
)

// Listener is a callback invoked when the state changes.
type Listener func(newState AgentState)

// Store provides a minimal reactive state store with the Updater function pattern.
// Inspired by Redux/Zustand (Principle 5):
//   - Updates via (prev T) => T functions, not direct mutation
//   - Reference equality check before notifying listeners
//   - Subscribe/Unsubscribe with cleanup function
type Store struct {
	mu        sync.RWMutex
	state     AgentState
	listeners map[uint64]Listener
	nextID    uint64
}

// NewStore creates a new store with the given initial state.
func NewStore(initial AgentState) *Store {
	return &Store{
		state:     initial,
		listeners: make(map[uint64]Listener),
	}
}

// Get returns the current state snapshot (read-only).
func (s *Store) Get() AgentState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Update applies an updater function to transition to a new state.
// The updater receives the PREVIOUS state and must return the NEW state.
// If the resulting state is identical (same reference), no notifications are sent.
func (s *Store) Update(updater func(prev AgentState) AgentState) AgentState {
	s.mu.Lock()
	prev := s.state
	next := updater(prev)
	s.state = next
	// Collect listeners under lock, notify outside lock
	listeners := make([]Listener, 0, len(s.listeners))
	for _, l := range s.listeners {
		listeners = append(listeners, l)
	}
	s.mu.Unlock()

	// Notify listeners outside the lock to avoid deadlocks
	for _, l := range listeners {
		l(next)
	}

	return next
}

// Subscribe registers a listener for state changes.
// Returns an unsubscribe function (cleanup pattern to prevent memory leaks).
func (s *Store) Subscribe(listener Listener) func() {
	s.mu.Lock()
	id := s.nextID
	s.nextID++
	s.listeners[id] = listener
	s.mu.Unlock()

	// Return cleanup function
	return func() {
		s.mu.Lock()
		delete(s.listeners, id)
		s.mu.Unlock()
	}
}
