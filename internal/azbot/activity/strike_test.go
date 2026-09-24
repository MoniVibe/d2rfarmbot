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

	if k, key := meleeStrike(ctx, s, 2, true, true); k != policy.Left || key != 0 {
		t.Fatalf("in reach: got %v key %#x, want the left hand with no key press", k, key)
	}
	if key := meleeAttackKey(ctx, s, 1); key != 0 {
		t.Fatalf("blind point-blank strike key = %#x, want 0 (SHIFT+left)", key)
	}
	if k, key := meleeStrike(ctx, s, 6, true, true); k != policy.Leap || key != 0x74 {
		t.Fatalf("beyond reach: got %v key %#x, want the Leap Attack gap-closer", k, key)
	}
	if k, _ := meleeStrike(ctx, s, 6, false, false); k != policy.Approach {
		t.Fatalf("beyond reach, hover dark, leap forbidden: got %v, want Approach", k)
	}
	leftAudit.BenchUntil = time.Now().Add(time.Minute)
	if k, key := meleeStrike(ctx, s, 2, true, true); k != policy.Swing || key != 0x72 {
		t.Fatalf("benched left: got %v key %#x, want the Double Swing fallback", k, key)
	}
	leftAudit = policy.LeftAudit{}
	ctx.Cap.Left.Disabled = true
	if k, key := meleeStrike(ctx, s, 2, true, true); k != policy.Swing || key != 0x72 {
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
	defer func() { leftAudit = policy.LeftAudit{} }()
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
	f.resolveStrikes(ctx, time.Now())
	// Strike 2: the same flinch never credits twice; the window runs out — deaf.
	f.noteStrike(ctx, 7, 0, true)
	f.resolveStrikes(ctx, time.Now())
	f.resolveStrikes(ctx, time.Now().Add(2*strikeEvidenceWindow))
	// Strike 3: a right-hand Double Swing that never reached a monster — whiff.
	f.noteStrike(ctx, 7, 0x72, false)

	got := strikeLines(led, "strike")
	want := []string{
		"skill=skill#999 mouse=left target=7 d=2 result=hit",
		"skill=skill#999 mouse=left target=7 d=2 result=deaf",
		"skill=Double Swing mouse=right target=7 d=2 result=whiff",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("strike lines:\n got %q\nwant %q", got, want)
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
