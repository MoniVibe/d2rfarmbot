package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/d2go/pkg/data/skill"
	"github.com/hectorgimenez/koolo/internal/azbot/combat"
	"github.com/hectorgimenez/koolo/internal/azbot/combat/policy"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// fableboi is the owner's 2026-09-24 kit: Carnage (a mod skill, named by id)
// on the left button, Leap Attack F5 and Double Swing F3 proven on the right.
func fableboi() *combat.Capability {
	leap := &combat.Binding{Key: 0x74, Skill: skill.ID(143)}
	double := &combat.Binding{Key: 0x72, Skill: skill.ID(133)}
	return &combat.Capability{LeapAttack: leap, DoubleSwing: double, Combat: leap, Contact: double,
		Left: &combat.LeftBinding{Skill: 999, Name: "skill#999", Mouse: "left", Proven: true}}
}

func TestMeleeStrikeLeftPrimary(t *testing.T) {
	defer func() { leftAudit = policy.LeftAudit{} }()
	ctx := &Ctx{Cap: fableboi()}
	s := &percept.Snapshot{Valid: true}
	s.Me.MaxMana, s.Me.MPPct = 40, 80

	if k, key := meleeStrike(ctx, s, 2, nil, true, true); k != policy.Left || key != 0 {
		t.Fatalf("in reach: got %v key %#x, want the left hand with no key press", k, key)
	}
	if key := meleeAttackKey(ctx, s, 1); key != 0 {
		t.Fatalf("blind point-blank strike key = %#x, want 0 (SHIFT+left)", key)
	}
	// A lone body beyond reach is Carnage's (the aimed left pursues); a
	// pack there is the leap's.
	if k, key := meleeStrike(ctx, s, 6, nil, true, true); k != policy.Left || key != 0 {
		t.Fatalf("lone target beyond reach: got %v key %#x, want the aimed left", k, key)
	}
	s.Me.Pos = data.Position{X: 100, Y: 100}
	for i, o := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {-1, 0}, {0, -1}, {1, 1}} {
		s.Enemies = append(s.Enemies, percept.EnemyRef{ID: data.UnitID(30 + i), Pos: data.Position{X: 105 + o[0], Y: 100 + o[1]}})
	}
	if k, key := meleeStrike(ctx, s, 4, nil, true, true); k != policy.Leap || key != 0x74 {
		t.Fatalf("pack of 6 at 5 tiles: got %v key %#x, want the Leap Attack", k, key)
	}
	s.Enemies = nil
	if k, _ := meleeStrike(ctx, s, 6, nil, false, false); k != policy.Approach {
		t.Fatalf("beyond reach, hover dark, leap forbidden: got %v, want Approach", k)
	}
	leftAudit.BenchUntil = time.Now().Add(time.Minute)
	if k, key := meleeStrike(ctx, s, 2, nil, true, true); k != policy.Swing || key != 0x72 {
		t.Fatalf("benched left: got %v key %#x, want the Double Swing fallback", k, key)
	}
	leftAudit = policy.LeftAudit{}
	ctx.Cap.Left.Disabled = true
	if k, key := meleeStrike(ctx, s, 2, nil, true, true); k != policy.Swing || key != 0x72 {
		t.Fatalf("-leftskill=off: got %v key %#x, want the pre-Carnage Double Swing", k, key)
	}
}

func strikeLines(led *verbs.Ledger, verb string) []string {
	var out []string
	for _, o := range led.Recent(256) {
		if o.Verb == verb {
			out = append(out, o.Evidence)
		}
	}
	return out
}

func TestStrikeTelemetry(t *testing.T) {
	defer func() { leftAudit, learner = policy.LeftAudit{}, nil }()
	learner = nil
	led := verbs.NewLedger(256)
	s := &percept.Snapshot{Valid: true}
	s.Me.Pos = data.Position{X: 100, Y: 100}
	s.Enemies = []percept.EnemyRef{{ID: 7, Pos: data.Position{X: 102, Y: 101}, Mode: uint32(mode.NpcStandingStill)}}
	ctx := &Ctx{Cap: fableboi(), Led: led, Snap: s}
	f := NewFight()
	f.target = 7
	t0 := time.Now()
	f.noteLock(ctx, t0)

	// Strike 1: the target ENTERS GettingHit — a hit.
	f.noteStrike(ctx, 7, 0, true)
	s.Enemies[0].Mode = uint32(mode.NpcGettingHit)
	f.resolveStrikes(ctx, time.Now().Add(200*time.Millisecond)) // past learn.Latency
	// Strike 2: the same flinch never credits twice; the window runs out — deaf.
	f.noteStrike(ctx, 7, 0, true)
	f.resolveStrikes(ctx, time.Now())
	f.resolveStrikes(ctx, time.Now().Add(strikeEvidenceWindow+100*time.Millisecond))
	// Strike 3: a right-hand Double Swing that never reached a monster — whiff.
	f.noteStrike(ctx, 7, 0x72, false)
	// The telemetry windows close: the issued strikes' lines are written.
	f.resolveStrikes(ctx, time.Now().Add(3*time.Second))

	got := strikeLines(led, "strike")
	want := []string{
		"skill=Double Swing mouse=right target=7 d=2 result=whiff", // a whiff is written at once
		"skill=skill#999 mouse=left target=7 d=2 result=hit aim=(102,101) n1=1 n2=1 n3=1 ",
		"skill=skill#999 mouse=left target=7 d=2 result=deaf aim=(102,101) n1=1 ",
	}
	if len(got) != len(want) {
		t.Fatalf("strike lines: %q", got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Fatalf("strike line %d:\n got %q\nwant prefix %q", i, got[i], want[i])
		}
	}
	if !strings.Contains(got[1], " hits=1 ") || !strings.Contains(got[1], " hpLoss=0 mp=0 next=") {
		t.Fatalf("telemetry fields: %q", got[1])
	}

	f.target = 0
	f.noteLock(ctx, t0.Add(3*time.Second))
	sum := strikeLines(led, "fight")
	if len(sum) != 1 || !strings.HasPrefix(sum[0], "fight summary: target=7 strikes=3 hits=1 duration=3.") ||
		!strings.HasSuffix(sum[0], "killed=false") {
		t.Fatalf("fight summary = %q", sum)
	}
}

func TestSilentLeftStrikesBenchCarnage(t *testing.T) {
	defer func() { leftAudit = policy.LeftAudit{} }()
	led := verbs.NewLedger(256)
	s := &percept.Snapshot{Valid: true}
	s.Enemies = []percept.EnemyRef{{ID: 9, Mode: uint32(mode.NpcStandingStill)}}
	ctx := &Ctx{Cap: fableboi(), Led: led, Snap: s}
	f := NewFight()
	for i := 0; i < policy.DeafLimit; i++ {
		f.noteStrike(ctx, 9, 0, true)
		f.resolveStrikes(ctx, time.Now().Add(2*strikeEvidenceWindow))
	}
	if !leftAudit.Benched(time.Now()) {
		t.Fatal("DeafLimit silent left strikes must bench the left skill")
	}
	if len(strikeLines(led, "fight")) != 1 {
		t.Fatalf("the bench must be written: %q", strikeLines(led, "fight"))
	}
}

// The leap's hard gates at the activity seam: a lone target's short gap is
// walked, a pack is leapt, a leap cools the next one, a wall vetoes it.
func TestMeleeStrikeLeapGates(t *testing.T) {
	defer func() { leftAudit, leapClock = policy.LeftAudit{}, policy.LeapClock{} }()
	ctx := &Ctx{Cap: fableboi()}
	s := &percept.Snapshot{Valid: true}
	s.Me.Pos = data.Position{X: 100, Y: 100}
	s.Me.MaxMana, s.Me.MPPct, s.Me.HPPct = 40, 80, 100

	if k, _ := meleeStrike(ctx, s, 5, nil, false, true); k != policy.Approach {
		t.Fatalf("lone d=5, hover dark: got %v, want Approach (walk the short gap)", k)
	}
	for i, o := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {-1, 0}, {0, -1}, {1, 1}, {-1, -1}} {
		s.Enemies = append(s.Enemies, percept.EnemyRef{ID: data.UnitID(20 + i), Pos: data.Position{X: 106 + o[0], Y: 100 + o[1]}})
	}
	d, key := meleeDecide(ctx, s, 5, nil, false, true, false)
	if d.Kind != policy.Leap || key != 0x74 || d.Leap.Cover != 7 {
		t.Fatalf("a pack of 7 at 6 tiles: got %v cover=%d; %s", d.Kind, d.Leap.Cover, d.Line())
	}
	noteLeap(ctx, 0x74)
	if k, _ := meleeStrike(ctx, s, 5, nil, false, true); k == policy.Leap {
		t.Fatal("leap cooling: a second leap fired")
	}
	leapClock = policy.LeapClock{}
	s.Me.MPPct = 5
	if k, _ := meleeStrike(ctx, s, 5, nil, false, true); k == policy.Leap {
		t.Fatal("dry pool: leapt anyway")
	}
}

// Every issued leap opens a telemetry window at its GROUND aim; the
// engagement summary and the AoE line close the pack.
func TestLeapTelemetryAndEngagement(t *testing.T) {
	defer func() { leftAudit, leapClock, learner = policy.LeftAudit{}, policy.LeapClock{}, nil }()
	learner = nil
	led := verbs.NewLedger(256)
	s := &percept.Snapshot{Valid: true}
	s.Me.Pos = data.Position{X: 100, Y: 100}
	s.Me.HPPct, s.Me.MPPct = 100, 200
	for i, o := range [][2]int{{0, 0}, {1, 0}, {0, 1}, {3, 0}} {
		s.Enemies = append(s.Enemies, percept.EnemyRef{ID: data.UnitID(40 + i), Pos: data.Position{X: 105 + o[0], Y: 100 + o[1]},
			Mode: uint32(mode.NpcStandingStill)})
	}
	ctx := &Ctx{Cap: fableboi(), Led: led, Snap: s}
	f := NewFight()
	aim := data.Position{X: 105, Y: 100}
	f.noteStrikeAt(ctx, 40, 0x74, true, &aim)
	if learner == nil || learner.T.Open() != 1 {
		t.Fatal("an issued leap must open a telemetry window")
	}
	t1 := time.Now().Add(300 * time.Millisecond)
	s.Enemies[1].Mode = uint32(mode.NpcGettingHit)
	s.Enemies = append(s.Enemies[:2:2], s.Enemies[3]) // 42 died on the landing tile's ring (no corpse read: vanished within 3)
	s.Me.MPPct = 175
	f.resolveStrikes(ctx, t1)
	// The pack is gone: quiet → the engagement ends.
	s.Enemies = nil
	f.resolveStrikes(ctx, t1.Add(200*time.Millisecond))
	f.resolveStrikes(ctx, t1.Add(1200*time.Millisecond))
	lines := strikeLines(led, "strike")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "skill=Leap Attack mouse=right target=40 d=5 result=") ||
		!strings.Contains(lines[0], "aim=(105,100) n1=3 ") || !strings.Contains(lines[0], " mp=25 ") {
		t.Fatalf("leap line: %q", lines)
	}
	var sum, aoe string
	for _, l := range strikeLines(led, "fight") {
		if strings.HasPrefix(l, "engage summary:") {
			sum = l
		}
		if strings.HasPrefix(l, "leap aoe:") {
			aoe = l
		}
	}
	if !strings.HasPrefix(sum, "engage summary: size=4 cleared=") || !strings.Contains(sum, " leaps=1 carnage=0 ") {
		t.Fatalf("engagement summary: %q", sum)
	}
	if !strings.HasPrefix(aoe, "leap aoe: r_est=3 measured=false prior=3 d0=") {
		t.Fatalf("aoe line: %q", aoe)
	}
	if learner.M.Count("leap", 2, 1) != 1 { // 4 bodies within 3 of the landing: the 4-6 bucket
		t.Fatalf("the leap sample must be learned: %s", learner.M.Table())
	}
}

// A body another strike already killed answers the next swing with
// "overkill" — never "deaf", never a strike against the left audit.
func TestOverkillIsNotSilence(t *testing.T) {
	defer func() { leftAudit, learner = policy.LeftAudit{}, nil }()
	learner = nil
	led := verbs.NewLedger(256)
	s := &percept.Snapshot{Valid: true}
	s.Me.Pos = data.Position{X: 100, Y: 100}
	s.Enemies = []percept.EnemyRef{{ID: 7, Pos: data.Position{X: 102, Y: 100}, Mode: uint32(mode.NpcStandingStill)}}
	ctx := &Ctx{Cap: fableboi(), Led: led, Snap: s}
	f := NewFight()
	f.target = 7
	f.noteLock(ctx, time.Now())
	f.noteStrike(ctx, 7, 0, true)
	f.noteStrike(ctx, 7, 0, true)
	s.Enemies = nil // one swing killed it: gone from the live list
	f.resolveStrikes(ctx, time.Now().Add(200*time.Millisecond))
	f.resolveStrikes(ctx, time.Now().Add(1600*time.Millisecond)) // the telemetry windows close
	got := strikeLines(led, "strike")
	want := []string{
		"skill=skill#999 mouse=left target=7 d=2 result=hit ",
		"skill=skill#999 mouse=left target=7 d=2 result=overkill ",
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], want[0]) || !strings.HasPrefix(got[1], want[1]) {
		t.Fatalf("strike lines:\n got %q\nwant prefixes %q", got, want)
	}
	// The kill is credited once: to the newest swing old enough to have landed it.
	if !strings.Contains(got[1], " kills=1 ") || !strings.Contains(got[0], " kills=0 ") {
		t.Fatalf("kill credit: %q", got)
	}
	if leftAudit.Deaf != 0 {
		t.Fatalf("overkill fed the left audit: deaf run %d", leftAudit.Deaf)
	}
	if !f.deathTaken[7] {
		t.Fatal("the kill must be recorded for the fight summary")
	}
}
