package learn

import (
	"fmt"
	"strings"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/memory"
)

// KV is the slice of the memory store the learner persists through
// (*memory.Store satisfies it: WAL append on Put, fsync by the Scribe ≤1s).
type KV interface {
	PutJSON(key string, scope memory.Scope, prov memory.Provenance, v any)
	GetJSON(key string, out any) bool
}

// StoreKey is the model's fact key: per character AND per skill set, so a
// respec or another character never inherits numbers measured on other hands.
func StoreKey(char, skillset string) string {
	char = strings.TrimSpace(char)
	if char == "" {
		char = "unknown"
	}
	return "combat.learn/" + char + "/" + skillset
}

// SkillSet names a kit for StoreKey: "L144+R143+R133" (left skill, leap,
// double swing; 0 = absent).
func SkillSet(left, leap, swing int) string {
	return fmt.Sprintf("L%d+R%d+R%d", left, leap, swing)
}

// Save writes the model as a forever-scoped, measured fact.
func (m *Model) Save(kv KV, key string) {
	if kv == nil || m == nil {
		return
	}
	n := 0
	for _, s := range m.Cells {
		n += s.N
	}
	kv.PutJSON(key, memory.ScopeForever, memory.Provenance{Source: "measured",
		Evidence: fmt.Sprintf("strike telemetry, %d strikes", n)}, m)
}

// LoadModel reads a saved model (an empty one when absent or unreadable).
func LoadModel(kv KV, key string) *Model {
	m := NewModel()
	if kv != nil && kv.GetJSON(key, m) {
		if m.Cells == nil {
			m.Cells = map[string]*Stats{}
		}
		for k, s := range m.Cells {
			if s == nil {
				delete(m.Cells, k)
			}
		}
		return m
	}
	return NewModel()
}

// SaveEvery: the model is written after this many new samples, and at every
// engagement end — a crash loses at most one engagement's tail.
const SaveEvery = 20

// Learner ties telemetry, engagement accounting, the model and its
// persistence together. One per character session.
type Learner struct {
	M   *Model
	T   *Tracker
	E   Engagement
	kv  KV
	key string
	// unsaved samples since the last Save.
	unsaved int
}

// NewLearner loads the model for key from kv (nil kv: memory only).
func NewLearner(kv KV, key string) *Learner {
	return &Learner{M: LoadModel(kv, key), T: NewTracker(), kv: kv, key: key}
}

// Key is the store key the learner persists under.
func (l *Learner) Key() string { return l.key }

// Strike opens a strike's telemetry; near is the enemy count within
// EngageRadius of the player (engagement size).
func (l *Learner) Strike(s *Strike, mons []Mon, near int) *Strike {
	l.T.Begin(s, mons)
	l.E.OnStrike(s.At, s.Skill, near)
	return s
}

// Observe feeds one tick. It returns the strikes closed this tick (already
// learned from) and, when the engagement ended, its summary.
func (l *Learner) Observe(now time.Time, hp, mp int, mons []Mon, near int) ([]*Strike, *Summary) {
	closed := l.T.Observe(now, hp, mp, mons)
	l.learn(closed)
	var sum *Summary
	if l.E.Tick(now, near) {
		rest := l.T.Flush(now)
		l.learn(rest)
		closed = append(closed, rest...)
		s := l.E.End(now)
		sum = &s
	}
	if sum != nil || l.unsaved >= SaveEvery {
		l.Save()
	}
	return closed, sum
}

func (l *Learner) learn(ss []*Strike) {
	for _, s := range ss {
		l.M.Observe(s.Sample())
		if s.Skill == Leap {
			l.M.AoE.Add(s.Exposed(), s.HitD)
		}
		l.E.OnClosed(s)
		l.unsaved++
	}
}

// Save persists the model now.
func (l *Learner) Save() {
	l.M.Save(l.kv, l.key)
	l.unsaved = 0
}

// AoELine is the leap splash measurement's log form.
func (l *Learner) AoELine() string {
	r, measured := l.M.AoE.Radius()
	return fmt.Sprintf("leap aoe: r_est=%d measured=%t prior=%d %s", r, measured, AoEPrior, l.M.AoE.String())
}
