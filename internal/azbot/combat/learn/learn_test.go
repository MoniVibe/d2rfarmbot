package learn

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/koolo/internal/azbot/memory"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestBuckets(t *testing.T) {
	for n, want := range map[int]int{0: 0, 1: 0, 2: 1, 3: 1, 4: 2, 6: 2, 7: 3, 30: 3} {
		if got := ClusterBucket(n); got != want {
			t.Errorf("ClusterBucket(%d)=%d want %d", n, got, want)
		}
	}
	for d, want := range map[int]int{0: 0, 3: 0, 4: 1, 5: 1, 6: 2, 9: 2, 10: 3, 40: 3} {
		if got := DistBucket(d); got != want {
			t.Errorf("DistBucket(%d)=%d want %d", d, got, want)
		}
	}
	if k := (Key{Leap, 2, 1}).String(); k != "leap/4-6/4-5" {
		t.Fatalf("key = %q", k)
	}
}

func TestShrinkage(t *testing.T) {
	// No data: the prior.
	if got := Shrink(0, 0, 1.5, PriorStrength, 0.9); !near(got, 1.5) {
		t.Fatalf("empty cell = %v, want the prior", got)
	}
	// k pseudo-actions of data at rate 0 halve the prior when the exposure
	// unit equals the prior action time.
	m := NewModel()
	p := PriorFor(Leap, 2, 1)
	for i := 0; i < int(PriorStrength); i++ {
		m.Observe(Sample{Skill: Leap, Cluster: 5, Dist: 4, ActionSec: p.ActionSec, Mana: 20})
	}
	e := m.Estimate(Leap, 2, 1)
	if !near(e.KillsPerSec, p.KillsPerSec/2) || e.N != 6 {
		t.Fatalf("half data/half prior: k/s=%v want %v (n=%d)", e.KillsPerSec, p.KillsPerSec/2, e.N)
	}
	if !near(e.ManaPerAction, (20*6+PriorStrength*p.ManaPerAction)/12) {
		t.Fatalf("mana/action = %v", e.ManaPerAction)
	}
	// Lots of data: the data.
	for i := 0; i < 2000; i++ {
		m.Observe(Sample{Skill: Leap, Cluster: 5, Dist: 4, ActionSec: 1, Kills: 3})
	}
	if e := m.Estimate(Leap, 2, 1); math.Abs(e.KillsPerSec-3) > 0.05 {
		t.Fatalf("data-dominated k/s = %v, want ≈3", e.KillsPerSec)
	}
	// Another cell is untouched.
	if e := m.Estimate(Leap, 3, 1); !near(e.KillsPerSec, PriorFor(Leap, 3, 1).KillsPerSec) || e.N != 0 {
		t.Fatalf("neighbour cell moved: %+v", e)
	}
	// A nil model is pure priors.
	var nm *Model
	if e := nm.Estimate(Carnage, 0, 0); !near(e.KillsPerSec, PriorFor(Carnage, 0, 0).KillsPerSec) {
		t.Fatal("nil model must answer priors")
	}
}

func TestPriorShape(t *testing.T) {
	for c := 1; c < NCluster; c++ {
		if PriorFor(Leap, c, 1).KillsPerSec <= PriorFor(Leap, c-1, 1).KillsPerSec {
			t.Fatal("leap prior must climb with the cluster")
		}
	}
	if PriorFor(Carnage, 0, 0).KillsPerSec <= PriorFor(Leap, 0, 0).KillsPerSec {
		t.Fatal("carnage must own the lone melee target")
	}
	if PriorFor(Leap, 2, 1).KillsPerSec <= PriorFor(Carnage, 2, 1).KillsPerSec {
		t.Fatal("leap must own a 4-6 pack at 4-5 tiles")
	}
	// The walk-in is ADDED to the action time.
	if a := PriorFor(Carnage, 0, 2).ActionSec; !near(a, 0.45+4.5/WalkTilesPerSec) {
		t.Fatalf("carnage 6-9 action time = %v", a)
	}
}

var t0 = time.Unix(1_790_000_000, 0)

func at(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }

const (
	idle   = uint32(mode.NpcStandingStill)
	hit    = uint32(mode.NpcGettingHit)
	knock  = uint32(mode.NpcKnockedBack)
	corpse = uint32(mode.NpcDeath)
)

func TestTrackerLeapSplash(t *testing.T) {
	tr := NewTracker()
	aim := Pt{10, 0}
	mons := []Mon{
		{ID: 1, P: Pt{10, 0}, Mode: idle}, // d0
		{ID: 2, P: Pt{11, 0}, Mode: idle}, // d1
		{ID: 3, P: Pt{10, 2}, Mode: idle}, // d2
		{ID: 4, P: Pt{13, 0}, Mode: idle}, // d3
		{ID: 5, P: Pt{15, 0}, Mode: idle}, // d5
		{ID: 6, P: Pt{30, 0}, Mode: idle}, // not watched
	}
	s := tr.Begin(&Strike{Skill: Leap, At: at(0), Me: Pt{3, 0}, Aim: aim, HP0: 100, MP0: 200}, mons)
	if s.D != 7 || s.N[0] != 1 || s.N[1] != 2 || s.N[3] != 4 || s.N[5] != 5 || s.N[6] != 5 {
		t.Fatalf("cluster counts: d=%d n=%v", s.D, s.N)
	}
	// 50ms: a flinch too young to be this leap's.
	tr.Observe(at(50), 100, 175, []Mon{{ID: 1, P: Pt{10, 0}, Mode: hit}, mons[1], mons[2], mons[3], mons[4]})
	if s.Hits() != 0 {
		t.Fatal("evidence inside Latency must not be credited")
	}
	// 400ms: 2 knocked back, 3 a corpse, 4 vanished (d3 → kill), 5 vanished (d5 → not provable), 1 still flinching (no new edge).
	tr.Observe(at(400), 97, 175, []Mon{{ID: 1, P: Pt{10, 0}, Mode: hit}, {ID: 2, P: Pt{12, 0}, Mode: knock},
		{ID: 3, P: Pt{10, 2}, Mode: corpse, Corpse: true}})
	if s.Hits() != 3 || s.Kills() != 2 {
		t.Fatalf("hits=%d kills=%d (hitd=%v killd=%v), want 3 and 2", s.Hits(), s.Kills(), s.HitD, s.KillD)
	}
	if closed := tr.Observe(at(1600), 97, 180, []Mon{{ID: 1, P: Pt{10, 0}, Mode: idle}, {ID: 2, P: Pt{12, 0}, Mode: idle}}); len(closed) != 1 || !closed[0].Closed {
		t.Fatalf("window must close at %v", Window)
	}
	if s.HPLoss() != 3 || s.Mana() != 25 {
		t.Fatalf("pools: hpLoss=%d mana=%d", s.HPLoss(), s.Mana())
	}
	if !strings.Contains(s.Fields(), "n1=2 n2=3 n3=4 n4=4 n5=5 n6=5 hits=3 kills=2") {
		t.Fatalf("fields: %s", s.Fields())
	}
	var h AoEHist
	h.Add(s.Exposed(), s.HitD)
	if h.Exposed[5] != 1 || h.Hit[5] != 0 || h.Hit[1] != 1 || h.Hit[2] != 1 || h.Hit[3] != 1 {
		t.Fatalf("aoe hist: %s", h.String())
	}
}

// A single-target swing owns its target's flinch; the older leap owns the
// splash nearer to its own landing. Time-to-next-action partitions the pools.
func TestTrackerCreditNearestImpactAndNextAction(t *testing.T) {
	tr := NewTracker()
	mons := []Mon{{ID: 1, P: Pt{10, 0}, Mode: idle}, {ID: 2, P: Pt{7, 0}, Mode: idle}}
	leap := tr.Begin(&Strike{Skill: Leap, At: at(0), Me: Pt{3, 0}, Aim: Pt{7, 0}, HP0: 100, MP0: 100}, mons)
	tr.Observe(at(200), 100, 75, mons) // the leap's mana
	carn := tr.Begin(&Strike{Skill: Carnage, At: at(500), Me: Pt{7, 0}, Aim: Pt{10, 0}, Target: 1, HP0: 100, MP0: 75}, mons)
	if !leap.NextAt.Equal(at(500)) || !near(leap.ActionSec(), 0.5) {
		t.Fatalf("time to next action: %v", leap.ActionSec())
	}
	tr.Observe(at(700), 90, 73, []Mon{{ID: 1, P: Pt{10, 0}, Mode: hit}, {ID: 2, P: Pt{7, 0}, Mode: hit}})
	if carn.Hits() != 1 || carn.HitD[0] != 0 || leap.Hits() != 1 || leap.HitD[0] != 0 {
		t.Fatalf("credit: carnage %v, leap %v", carn.HitD, leap.HitD)
	}
	tr.Flush(at(900))
	if leap.HPLoss() != 0 || leap.Mana() != 25 || carn.HPLoss() != 10 || carn.Mana() != 2 {
		t.Fatalf("pool partition: leap hp=%d mp=%d, carnage hp=%d mp=%d", leap.HPLoss(), leap.Mana(), carn.HPLoss(), carn.Mana())
	}
	if carn.NextAt.IsZero() && !near(carn.ActionSec(), PriorFor(Carnage, 0, 0).ActionSec) {
		t.Fatal("no next action: the prior action time")
	}
	// A death is credited once, ever.
	tr2 := NewTracker()
	a := tr2.Begin(&Strike{Skill: Carnage, At: at(0), Aim: Pt{1, 0}, Target: 9}, []Mon{{ID: 9, P: Pt{1, 0}, Mode: idle}})
	b := tr2.Begin(&Strike{Skill: Carnage, At: at(300), Aim: Pt{1, 0}, Target: 9}, []Mon{{ID: 9, P: Pt{1, 0}, Mode: idle}})
	tr2.Observe(at(600), 0, 0, []Mon{{ID: 9, P: Pt{1, 0}, Mode: corpse, Corpse: true}})
	tr2.Observe(at(700), 0, 0, nil)
	if a.Kills()+b.Kills() != 1 || b.Kills() != 1 {
		t.Fatalf("one death, one credit (to the newest on a tie): a=%d b=%d", a.Kills(), b.Kills())
	}
}

// A window nobody watched (the Fight lost the wheel) closes unjudged.
func TestTrackerStaleWindow(t *testing.T) {
	tr := NewTracker()
	s := tr.Begin(&Strike{Skill: Leap, At: at(0), Aim: Pt{5, 0}}, []Mon{{ID: 1, P: Pt{5, 0}, Mode: idle}})
	closed := tr.Observe(at(int((Window+StaleGrace)/time.Millisecond)), 0, 0, nil)
	if len(closed) != 1 || s.Kills() != 0 || s.Hits() != 0 {
		t.Fatalf("stale window: closed=%d kills=%d", len(closed), s.Kills())
	}
}

func TestAoERadius(t *testing.T) {
	var h AoEHist
	if r, measured := h.Radius(); r != AoEPrior || measured {
		t.Fatalf("no data: r=%d measured=%t, want the prior", r, measured)
	}
	// A wide splash: everything to 5 tiles flinches, 6 does not.
	for i := 0; i < 40; i++ {
		h.Add([]float64{0, 1, 2, 3, 4, 5, 6}, []float64{0, 1, 2, 3, 4, 5})
	}
	if r, measured := h.Radius(); r != 5 || !measured {
		t.Fatalf("wide splash: r=%d measured=%t, want 5", r, measured)
	}
	// A tight splash: only the landing tile and its ring.
	var g AoEHist
	for i := 0; i < 40; i++ {
		g.Add([]float64{0, 1, 2, 3}, []float64{0, 1})
	}
	if r, _ := g.Radius(); r != 1 {
		t.Fatalf("tight splash: r=%d, want 1", r)
	}
}

func TestEngagement(t *testing.T) {
	var e Engagement
	e.OnStrike(at(0), Leap, 8)
	e.OnStrike(at(400), Carnage, 6)
	e.OnStrike(at(800), Carnage, 3)
	e.OnClosed(&Strike{KillD: []float64{1, 2}, HitD: []float64{1, 2}, HP0: 50, hpMin: 45, MP0: 100, mpMin: 75})
	if e.Tick(at(900), 2) || e.Tick(at(1000), 0) {
		t.Fatal("still fighting / not quiet long enough")
	}
	if !e.Tick(at(1000+int(EngageQuiet/time.Millisecond)), 0) {
		t.Fatal("quiet for EngageQuiet must end it")
	}
	s := e.End(at(1750))
	if got := s.String(); got != "engage summary: size=8 cleared=2 dur=1.8s hpLoss=5 mp=25 leaps=1 carnage=2 ds=0 basic=0" {
		t.Fatalf("summary: %s", got)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { st.Scribe(stop); close(done) }()
	key := StoreKey("Fableboi", SkillSet(144, 143, 133))
	l := NewLearner(st, key)
	l.M.Observe(Sample{Skill: Leap, Cluster: 5, Dist: 5, ActionSec: 0.8, Kills: 2, Hits: 4, HPLoss: 3, Mana: 25})
	l.M.Observe(Sample{Skill: Carnage, Cluster: 1, Dist: 2, ActionSec: 0.4, Kills: 1, Hits: 1})
	l.M.AoE.Add([]float64{0, 2, 4}, []float64{0, 2})
	l.Save()
	close(stop) // the Scribe's final flush + fsync
	<-done
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	re, err := memory.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	f, ok := re.Get(key)
	if !ok || f.Scope != memory.ScopeForever || f.Prov.Source != "measured" {
		t.Fatalf("fact: ok=%t %+v", ok, f)
	}
	m := LoadModel(re, key)
	if m.Count(Leap, 2, 1) != 1 || m.Count(Carnage, 0, 0) != 1 || m.AoE.Hit[2] != 1 || m.AoE.Exposed[4] != 1 {
		t.Fatalf("round trip lost data: %+v", m)
	}
	if !near(m.Estimate(Leap, 2, 1).KillsPerSec, l.M.Estimate(Leap, 2, 1).KillsPerSec) {
		t.Fatal("estimates differ after the round trip")
	}
	if LoadModel(re, StoreKey("Other", "L0+R143+R0")).Count(Leap, 2, 1) != 0 {
		t.Fatal("another character/skill set must start empty")
	}
}

// The learner learns every closed strike and saves at engagement end.
func TestLearnerLoop(t *testing.T) {
	kv := &mapKV{m: map[string]any{}}
	l := NewLearner(kv, "k")
	mons := []Mon{{ID: 1, P: Pt{2, 0}, Mode: idle}}
	l.Strike(&Strike{Skill: Carnage, At: at(0), Aim: Pt{2, 0}, Target: 1, HP0: 100, MP0: 100}, mons, 1)
	l.Observe(at(300), 100, 100, []Mon{{ID: 1, P: Pt{2, 0}, Mode: corpse, Corpse: true}}, 0)
	closed, sum := l.Observe(at(1200), 100, 100, nil, 0)
	if sum == nil || sum.Cleared != 1 || sum.Carnage != 1 || len(closed) != 1 {
		t.Fatalf("engagement end: sum=%+v closed=%d", sum, len(closed))
	}
	if l.M.Count(Carnage, 0, 0) != 1 || kv.puts != 1 {
		t.Fatalf("learned=%d saves=%d", l.M.Count(Carnage, 0, 0), kv.puts)
	}
}

type mapKV struct {
	m    map[string]any
	puts int
}

func (k *mapKV) PutJSON(key string, _ memory.Scope, _ memory.Provenance, v any) {
	k.m[key] = v
	k.puts++
}
func (k *mapKV) GetJSON(key string, out any) bool { return false }
