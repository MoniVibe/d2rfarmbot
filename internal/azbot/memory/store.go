// Package memory is azbot's learning substrate: LAW 5 — experience survives its
// boundary. Facts carry scope and provenance; Puts append to a WAL the Scribe fsyncs
// within a second, so a wedge learned at 03:12 survives the 03:13 crash.
package memory

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Scope uint8

const (
	ScopeTick Scope = iota
	ScopeActivity
	ScopeArea
	ScopeGame // one game session (a death_state, progression gauges)
	ScopeSeed // this map seed (wedges, roads, anchors)
	ScopeForever
)

type Provenance struct {
	Source   string `json:"src"` // measured | derived | prior | proven-negative | hand-piloted
	Evidence string `json:"ev,omitempty"`
}

type Fact struct {
	Key   string          `json:"k"`
	Scope Scope           `json:"s"`
	Prov  Provenance      `json:"p"`
	Val   json.RawMessage `json:"v"`
	At    time.Time       `json:"t"`
}

// Store: in-memory map + append-only WAL. Load replays the WAL; Compact rewrites it.
type Store struct {
	mu    sync.RWMutex
	facts map[string]Fact
	wal   *os.File
	w     *bufio.Writer
	dirty chan struct{}
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "facts.wal")
	s := &Store{facts: make(map[string]Fact), dirty: make(chan struct{}, 1)}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
		for sc.Scan() {
			var fa Fact
			if json.Unmarshal(sc.Bytes(), &fa) == nil {
				s.facts[fa.Key] = fa
			}
		}
		f.Close()
	}
	wal, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	s.wal, s.w = wal, bufio.NewWriter(wal)
	return s, nil
}

// Put records a fact and signals the Scribe. Crash-safety comes from Scribe's fsync
// cadence (≤1s), not from blocking the caller.
func (s *Store) Put(f Fact) {
	if f.At.IsZero() {
		f.At = time.Now()
	}
	s.mu.Lock()
	s.facts[f.Key] = f
	b, _ := json.Marshal(f)
	s.w.Write(b)
	s.w.WriteByte('\n')
	s.mu.Unlock()
	select {
	case s.dirty <- struct{}{}:
	default:
	}
}

func (s *Store) Get(key string) (Fact, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	f, ok := s.facts[key]
	return f, ok
}

// PutJSON marshals v as the fact value.
func (s *Store) PutJSON(key string, scope Scope, prov Provenance, v any) {
	b, _ := json.Marshal(v)
	s.Put(Fact{Key: key, Scope: scope, Prov: prov, Val: b})
}

func (s *Store) GetJSON(key string, out any) bool {
	f, ok := s.Get(key)
	if !ok {
		return false
	}
	return json.Unmarshal(f.Val, out) == nil
}

// Scribe runs the write-behind fsync loop. Call in its own goroutine; returns on stop.
func (s *Store) Scribe(stop <-chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	flush := func() {
		s.mu.Lock()
		s.w.Flush()
		s.wal.Sync()
		s.mu.Unlock()
	}
	for {
		select {
		case <-stop:
			flush()
			return
		case <-s.dirty:
			flush()
		case <-t.C:
			flush()
		}
	}
}

// DropScope removes all facts at or below the given scope (e.g. new game session drops
// ScopeGame and tighter). The WAL keeps history; Compact would rewrite it (later).
func (s *Store) DropScope(max Scope) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, f := range s.facts {
		if f.Scope <= max {
			delete(s.facts, k)
		}
	}
}
