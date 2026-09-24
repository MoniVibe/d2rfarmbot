package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// Step 7 (docs/AZBOT_V2.md): the town services on contract v2. These tests
// drive the phase tables and the shared lifecycle on fake contexts — a Ctx
// with no motor and no memory reader, so any click or key a path makes would
// panic: the absence of a panic is the proof that a path clicks nothing.

func withJanitor(t *testing.T, on bool) {
	t.Helper()
	was := JanitorOn
	JanitorOn = on
	t.Cleanup(func() { JanitorOn = was })
}

func fakeCtx(s *percept.Snapshot) *Ctx {
	if s == nil {
		s = &percept.Snapshot{Valid: true}
	}
	s.Me.InTown = true
	return &Ctx{Led: verbs.NewLedger(64), Snap: s}
}

// reading builds a stable, janitor-shaped Reading with panels positively seen.
func reading(mode screen.Mode, seen screen.Panel) *screen.Reading {
	return &screen.Reading{Mode: mode, Panels: seen, Sight: seen,
		Evidence: map[screen.Panel]string{}, Close: map[screen.Panel]screen.Point{}}
}

// seenAt publishes r as an Eye observation taken at t (belief agreeing).
func seenAt(r *screen.Reading, t time.Time) *exec.Seen {
	return &exec.Seen{At: t, State: r.State(), Reading: *r}
}

func TestErrandClaimsPerPhase(t *testing.T) {
	for p := erClean; p <= erClose; p++ {
		want := screen.Panel(0)
		if p >= erTalk {
			want = claimsVendor
		}
		if got := errandClaims(p); got != want {
			t.Errorf("%s claims %s, want %s", p, got, want)
		}
		if p.String() == "?" {
			t.Errorf("phase %d has no name", p)
		}
	}
	// Walking claims nothing: a menu a walk-click opened is foreign.
	if errandClaims(erSeek)&screen.NPCMenu != 0 || errandClaims(erApproach) != 0 {
		t.Fatal("the walk must not claim NPC panels")
	}
}

func TestSpendClaimsNeverAVendor(t *testing.T) {
	for p := spClean; p <= spClose; p++ {
		c := spendClaims(p)
		if c&(screen.Shop|screen.NPCMenu|screen.NPCDialog|screen.Inventory) != 0 {
			t.Errorf("%s claims %s: Spend must never own a vendor, NPC or bag panel", p, c)
		}
		if p.String() == "?" {
			t.Errorf("phase %d has no name", p)
		}
	}
	if spendClaims(spClean) != 0 {
		t.Fatal("Clean claims nothing")
	}
	// The stat phases leave the tree foreign and vice versa: between the two
	// doors the sheet is closed by sight, never toggled.
	if spendClaims(spStats)&claimsTree != 0 || spendClaims(spSkills)&claimsSheet != 0 {
		t.Fatal("stat and skill phases must not claim each other's panel")
	}
}

func TestBagClaims(t *testing.T) {
	if bagClaims(idClean) != 0 || bagClaims(eqClean) != 0 {
		t.Fatal("Clean claims nothing")
	}
	for _, p := range []idPhase{idCast, idTouch, idClose} {
		if bagClaims(p) != claimsBag {
			t.Errorf("identify %s claims %s", p, bagClaims(p))
		}
	}
	for _, p := range []eqPhase{eqDoor, eqDress, eqClose} {
		if bagClaims(p) != claimsBag {
			t.Errorf("equip %s claims %s", p, bagClaims(p))
		}
	}
	// Relay R10: under a cursor item Equip's Clean keeps the bag AND the
	// trade frame beside it (closing the trade window takes the bag with it).
	if cleanKeep(true) != claimsBag|screen.Shop|screen.LeftPanel || cleanKeep(false) != 0 {
		t.Fatal("Equip's Clean keeps the bag (and a trade frame) only under a cursor item")
	}
	eq := NewEquip()
	s := &percept.Snapshot{Valid: true}
	s.Me.CursorItem = true
	eq.life.begin(false)
	eq.life.to(eqDress, "test")
	if eq.Needs(s).Claims&screen.Shop == 0 {
		t.Fatal("Dress under a cursor item keeps the trade window that holds the bag open")
	}
	s.Me.CursorItem = false
	if eq.Needs(s).Claims&screen.Shop != 0 {
		t.Fatal("Dress without a cursor item: a vendor is foreign (a shift-click would SELL)")
	}
}

func TestErrandResumeTable(t *testing.T) {
	cases := []struct {
		p                   errandPhase
		janitor, menu, shop bool
		want                errandPhase
	}{
		{erSeek, true, false, false, erSeek},
		{erApproach, false, false, false, erApproach},
		{erTalk, true, true, false, erTalk}, // Talk re-checks itself (stray menu → Clean)
		{erMenu, false, true, false, erMenu},
		{erMenu, false, false, false, erClean},
		{erMenu, true, true, false, erClean}, // janitor ON: the menu was closed on suspend
		{erOpening, false, false, true, erOpening},
		{erAct, false, false, true, erAct},
		{erAct, false, true, false, erClean},
		{erAct, true, false, true, erClean},
		{erClose, false, false, false, erClose},
	}
	for _, c := range cases {
		if got := errandResume(c.p, c.janitor, c.menu, c.shop); got != c.want {
			t.Errorf("resume(%s, janitor=%v menu=%v shop=%v) = %s, want %s", c.p, c.janitor, c.menu, c.shop, got, c.want)
		}
	}
	if bagResume(idTouch, idClose) != idClean || bagResume(idClose, idClose) != idClose ||
		bagResume(eqDress, eqClose) != eqClean {
		t.Fatal("bag rituals resume from Clean; Close stays Close")
	}
}

func TestSpendDecisions(t *testing.T) {
	if got := spendNext(3, true, 2, true); got != spStatDoor {
		t.Errorf("stats first: %s", got)
	}
	if got := spendNext(3, false, 2, true); got != spSkillDoor {
		t.Errorf("retired stats → skills: %s", got)
	}
	if got := spendNext(0, true, 0, true); got != spClean {
		t.Errorf("nothing banked → done: %s", got)
	}
	// Plan B: the hotkey TOGGLES — only with the panel SEEN closed.
	cases := []struct {
		frozen      int
		sighted, up bool
		want        planB
	}{
		{1, true, false, planHotkey},
		{1, true, true, planRetry},   // the sheet is up: 'C' would close it
		{1, false, false, planRetry}, // no reading: never a blind key
		{2, true, false, planRetry},
		{3, true, false, planRetire},
	}
	for _, c := range cases {
		if got := spendPlanB(c.frozen, c.sighted, c.up); got != c.want {
			t.Errorf("planB(frozen=%d sighted=%v up=%v) = %d, want %d", c.frozen, c.sighted, c.up, got, c.want)
		}
	}
	if x, y := statRecipient(10, 20, 10, 30); x != strBtnX || y != strBtnY {
		t.Error("strength gate first")
	}
	if x, y := statRecipient(20, 20, 10, 30); x != dexBtnX || y != dexBtnY {
		t.Error("then dexterity")
	}
	if x, y := statRecipient(20, 20, 30, 30); x != vitBtnX || y != vitBtnY {
		t.Error("the rest to vitality")
	}
}

func TestTransactionVerdicts(t *testing.T) {
	if judgeBuy(2, 3, 100, 70) != buyOK || judgeBuy(2, 2, 100, 70) != buyWrong || judgeBuy(2, 2, 100, 100) != buyDeaf {
		t.Fatal("buy verdicts")
	}
	if judgeSell(7, []data.UnitID{7}) != sellOK || judgeSell(7, []data.UnitID{8}) != sellWrong ||
		judgeSell(7, []data.UnitID{7, 8}) != sellWrong || judgeSell(7, nil) != sellDeaf {
		t.Fatal("sell verdicts: EXACTLY this item gone")
	}
	for d, want := range map[int]int{2: -1, 3: -1, 4: 0, 7: 0, 8: 1, 20: 1} {
		if got := talkBand(d); got != want {
			t.Errorf("talkBand(%d) = %d, want %d", d, got, want)
		}
	}
	now := time.Now()
	if talkedRecently(time.Time{}, now) || talkedRecently(now.Add(-6*time.Second), now) || !talkedRecently(now.Add(-time.Second), now) {
		t.Fatal("a menu is ours only within 5s of our talk click")
	}
	var occ [bagCols][bagRows]bool
	for x := 0; x < 10; x++ {
		occ[x][0] = true
	}
	occ[0][1] = true
	if gx, gy, ok := parkRegion(&occ, 2, 2); !ok || gx != 1 || gy != 1 {
		t.Fatalf("parkRegion 2x2 = (%d,%d,%v), want (1,1,true)", gx, gy, ok)
	}
	for x := 0; x < 10; x++ {
		for y := 0; y < 4; y++ {
			occ[x][y] = true
		}
	}
	// Relay R10: the vanilla 10x4 scan read this bag as full ("would not
	// park (no room)"); the mod's bag is 10x8 — rows 4-7 are room.
	if gx, gy, ok := parkRegion(&occ, 2, 3); !ok || gx != 0 || gy != 4 {
		t.Fatalf("parkRegion 2x3 under a full top half = (%d,%d,%v), want (0,4,true)", gx, gy, ok)
	}
	for x := 0; x < bagCols; x++ {
		for y := 0; y < bagRows; y++ {
			occ[x][y] = true
		}
	}
	if _, _, ok := parkRegion(&occ, 1, 1); ok {
		t.Fatal("a full bag has no region")
	}
	// The footprint the item came from is the preferred region when free.
	occ = [bagCols][bagRows]bool{}
	occ[3][5] = true
	if regionFree(&occ, 2, 4, 2, 2) || !regionFree(&occ, 4, 4, 2, 2) || regionFree(&occ, 9, 7, 2, 1) {
		t.Fatal("regionFree")
	}
}

// The lifecycle: Clean claims nothing, a claiming phase keeps the bid, and a
// terminal verdict carries its Reason.
func TestLifecycleClaimsAndKeptBid(t *testing.T) {
	withJanitor(t, true)
	r := NewRestock()
	ctx := fakeCtx(nil)
	r.Begin(ctx, false)
	if n := r.Needs(ctx.Snap); n.Claims != 0 || n.Cursor != exec.CursorOwn || !n.Mode.Allows(screen.World) {
		t.Fatalf("Clean needs: %+v", n)
	}
	bid := &arbiter.Demand{Who: "restock", Class: arbiter.ClassService, Urgency: 0.5}
	if r.e.keepBid(bid, ctx.Snap) != bid {
		t.Fatal("a live bid passes through")
	}
	if r.e.keepBid(nil, ctx.Snap) != nil {
		t.Fatal("Clean owns nothing: no kept bid")
	}
	r.e.to(erAct, "test")
	if r.Needs(ctx.Snap).Claims != claimsVendor {
		t.Fatal("Act claims the vendor set")
	}
	kept := r.e.keepBid(nil, ctx.Snap)
	if kept == nil || kept.Who != "restock" || kept.Urgency != PanelLockUrgency {
		t.Fatalf("the shop is ours: the bid is kept, at the panel lock (%v)", kept)
	}
	if live := r.e.keepBid(bid, ctx.Snap); live.Urgency != PanelLockUrgency || bid.Urgency != 0.5 {
		t.Fatalf("a live bid at our panel rides the lock (%v), the caller's demand untouched (%v)", live, bid)
	}
	ctx.Snap.Me.InTown = false
	if r.e.keepBid(nil, ctx.Snap) != nil {
		t.Fatal("never kept outside town")
	}
	ctx.Snap.Me.InTown = true
	// Janitor ON: a terminal verdict is immediate, typed, and End resets.
	st := r.e.finish(ctx, phase.Done, phase.Completed, "test")
	if st.V != phase.Done || st.Why != phase.Completed || st.Phase != "Act" {
		t.Fatalf("finish: %+v", st)
	}
	r.End(ctx, st.V, st.Why)
	if r.e.ph.Phase() != erClean || r.e.live || r.e.keepBid(nil, ctx.Snap) != nil {
		t.Fatal("End resets the episode")
	}
}

// Janitor OFF: the verdict is latched while the Close phase closes our panels
// by sight; a clear, fresh reading releases it.
func TestCloseLatchesVerdictUntilClear(t *testing.T) {
	withJanitor(t, false)
	idn := NewIdentify()
	ctx := fakeCtx(nil)
	idn.Begin(ctx, false)
	idn.life.to(idTouch, "test")
	// A reading off the world: the sight closer may not act (no motor is
	// wired — an action would panic), so the verdict waits.
	ctx.Seen = seenAt(reading(screen.Unknown, 0), time.Now())
	st := idn.life.finish(ctx, phase.Done, phase.Completed, "backlog identified")
	if st.V != phase.Wait || st.WakeAt.IsZero() || idn.life.ph.Phase() != idClose {
		t.Fatalf("latched: %+v in %s", st, idn.life.ph.Phase())
	}
	// A clear reading taken too soon after entering Close cannot judge.
	ctx.Seen = seenAt(reading(screen.World, 0), time.Now())
	if st := idn.Step(ctx); st.V != phase.Wait {
		t.Fatalf("a stale clear reading must not release the verdict: %+v", st)
	}
	// A fresh clear reading releases it, with its reason.
	ctx.Seen = seenAt(reading(screen.World, 0), time.Now().Add(time.Second))
	st = idn.Step(ctx)
	if st.V != phase.Done || st.Why != phase.Completed || st.Evidence != "backlog identified" {
		t.Fatalf("released: %+v", st)
	}
}

// THE LIVE BUG (2026-09-24): Fence left Drognan's trade window open and Spend
// clicked New Stats into it. Now Spend's door phase sees the shop and goes
// back to Clean without a single click (the fake has no motor).
func TestSpendVendorInterlock(t *testing.T) {
	for _, jan := range []bool{false, true} {
		withJanitor(t, jan)
		sp := NewSpend()
		s := &percept.Snapshot{Valid: true}
		s.Me.StatPoints = 5
		ctx := fakeCtx(s)
		sp.Begin(ctx, false)
		sp.life.to(spStatDoor, "test")
		shop := reading(screen.World, screen.Shop|screen.Inventory)
		if jan {
			ctx.Screen = shop
		} else {
			ctx.Seen = seenAt(shop, time.Now())
		}
		st := sp.Step(ctx)
		if sp.life.ph.Phase() != spClean || st.V != phase.Running {
			t.Fatalf("janitor=%v: a shop on screen must send Spend back to Clean, got %s %+v", jan, sp.life.ph.Phase(), st)
		}
		if n := sp.Needs(s); n.Claims != 0 {
			t.Fatalf("janitor=%v: Clean claims nothing, so the janitor closes the shop (claims %s)", jan, n.Claims)
		}
		// Janitor ON: Clean waits behind the gate while the shop is seen.
		if jan {
			if st := sp.Step(ctx); st.V != phase.Wait || sp.life.ph.Phase() != spClean {
				t.Fatalf("Clean with a shop up: %+v", st)
			}
		}
	}
}

// Clean advances only on a clean screen, then picks the door.
func TestSpendCleanPicksDoor(t *testing.T) {
	withJanitor(t, true)
	sp := NewSpend()
	s := &percept.Snapshot{Valid: true}
	s.Me.SkillPoints = 1
	ctx := fakeCtx(s)
	ctx.Screen = reading(screen.World, 0)
	sp.Begin(ctx, false)
	if st := sp.Step(ctx); st.V != phase.Running || sp.life.ph.Phase() != spSkillDoor {
		t.Fatalf("skills banked → SkillDoor, got %s %+v", sp.life.ph.Phase(), st)
	}
	sp.End(ctx, phase.Abandoned, phase.Preempted)
	s.Me.SkillPoints = 0
	sp.Begin(ctx, false)
	if st := sp.Step(ctx); st.V != phase.Done || st.Why != phase.Completed {
		t.Fatalf("nothing banked → Done(completed), got %+v", st)
	}
}

// A phase over its held-time budget is Abandoned(Timebox).
func TestPhaseBudgetTimebox(t *testing.T) {
	withJanitor(t, true)
	sp := NewSpend()
	s := &percept.Snapshot{Valid: true}
	s.Me.StatPoints = 5
	ctx := fakeCtx(s)
	held := time.Duration(0)
	ctx.Held = func(string) time.Duration { return held }
	ctx.Screen = reading(screen.World, screen.Shop) // Clean waits on it forever
	sp.Begin(ctx, false)
	if st := sp.Step(ctx); st.V != phase.Wait {
		t.Fatalf("Clean waits: %+v", st)
	}
	held = 11 * time.Second
	st := sp.Step(ctx)
	if st.V != phase.Abandoned || st.Why != phase.Timebox || !strings.Contains(st.Evidence, "Clean") {
		t.Fatalf("overrun: %+v", st)
	}
}

// Begin(resumed) re-verifies: a bag ritual comes back to Clean, a fresh Begin
// resets the counters.
func TestResumeRegresses(t *testing.T) {
	withJanitor(t, true)
	idn := NewIdentify()
	ctx := fakeCtx(nil)
	idn.Begin(ctx, false)
	idn.life.to(idTouch, "test")
	idn.tries = 2
	idn.Suspend(ctx, phase.Preempted) // MoveStop on a nil motor: no-op, no clicks
	idn.Begin(ctx, true)
	if idn.life.ph.Phase() != idClean || idn.tries != 2 {
		t.Fatalf("resumed: %s tries=%d", idn.life.ph.Phase(), idn.tries)
	}
	r := NewRestock()
	r.Begin(ctx, false)
	r.e.to(erAct, "test")
	r.e.tradeSelected = true
	r.Begin(ctx, true)
	if r.e.ph.Phase() != erClean || r.e.tradeSelected {
		t.Fatalf("resumed errand (janitor ON): %s tradeSelected=%v", r.e.ph.Phase(), r.e.tradeSelected)
	}
}

// Every service registered in the roster is a native v2 Life, not Legacy.
func TestServicesAreNativeV2(t *testing.T) {
	r := Registry(nil, nil)
	for _, name := range []string{"identify", "equip", "spend", "heal", "repair", "restock", "fence"} {
		a := r.Get(name)
		if a == nil {
			t.Fatalf("%s not registered", name)
		}
		if _, legacy := a.(*Legacy); legacy {
			t.Errorf("%s still rides the Legacy adapter", name)
		}
		if p, ok := a.(Phased); !ok || p.PhaseName() != "Clean" {
			t.Errorf("%s: phase %v, want Clean at rest", name, a)
		}
	}
}
