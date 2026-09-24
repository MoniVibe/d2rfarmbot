package activity

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/stat"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/verbs"
)

// ---------------------------------------------------------------- Spend (ClassService)

// spendWorks: the live belief that the New-Stats screen button and the panel's
// plus buttons spend points here. Retired on frozen counts, like equipWorks.
var spendWorks atomic.Bool

func init() { spendWorks.Store(true) }

// skillSpendWorks: the live belief that the tree door + calibrated grid spend
// skill points here (P-8.7). Retired on frozen counts.
var skillSpendWorks atomic.Bool

func init() { skillSpendWorks.Store(true) }

// farmbot's calibrated skill-tree grid, client pixels — PROVEN on this repack
// (-autoskill spent Benji's banked points into Teeth, 2026-07-18). Page 1 is
// the RIGHTMOST tab. Data with provenance, not class knowledge (rule 5).
var (
	skillTabX = map[int]int{1: 1668, 2: 1523, 3: 1380}
	skillColX = [4]int{0, 1397, 1524, 1651}
	skillRowY = [7]int{0, 279, 365, 449, 529, 625, 711}
)

const skillTabY = 201

// Screen geometry, client pixels (1920x1050): the New Stats button photographed
// live 2026-07-19 08:21 (logs/shot.png, physical 475,793 / 1.2); the plus
// buttons are farmbot's proven -statalloc coords on this same window.
const (
	newStatsBtnX, newStatsBtnY = 397, 662
	// Photographed at 12:20 with a CLEAN HUD (logs/talk_fail.png): the button's
	// home is right of the belt; the 12:12 left-side reading was its panel-open
	// relocation (D2R moves it clear of an open inventory).
	newSkillBtnX, newSkillBtnY = 1377, 763
	strBtnX, strBtnY           = 347, 305
	dexBtnX, dexBtnY           = 347, 428
	vitBtnX, vitBtnY           = 347, 552
)

// spPhase is Spend's phase (contract v2).
type spPhase uint8

const (
	spClean     spPhase = iota // precondition: nothing on screen; then the next door (stats, then skills)
	spStatDoor                 // the New Stats button opens the char sheet (only when it is not seen)
	spStats                    // one plus click on the recipient, judged by the StatPoints delta
	spSkillDoor                // the New Skill button opens the tree (only when it is not seen)
	spSkills                   // tab, seat, judged by the SkillPoints delta
	spClose                    // janitor OFF: the sheet/tree closed by sight
)

func (p spPhase) String() string {
	switch p {
	case spClean:
		return "Clean"
	case spStatDoor:
		return "StatDoor"
	case spStats:
		return "Stats"
	case spSkillDoor:
		return "SkillDoor"
	case spSkills:
		return "Skills"
	case spClose:
		return "Close"
	}
	return "?"
}

// Per-phase claims: the char sheet reads as the left half-panel, the tree as
// the right one. The stats phases never claim the tree, nor the skill phases
// the sheet — so between the two the sheet is foreign and closed by sight
// (the old disown), and NOTHING else (a vendor window, an NPC menu, the bag)
// is ever claimed by Spend.
const (
	claimsSheet = screen.CharSheet | screen.LeftPanel
	claimsTree  = screen.SkillTree | screen.RightPanel
)

func spendClaims(p spPhase) screen.Panel {
	switch p {
	case spStatDoor, spStats:
		return claimsSheet
	case spSkillDoor, spSkills:
		return claimsTree
	case spClose:
		return claimsSpend
	}
	return 0
}

// statRecipient (P-8.3): strength to the gate, then dexterity, then vitality.
func statRecipient(str, needStr, dex, needDex int) (x, y int) {
	switch {
	case needStr > str:
		return strBtnX, strBtnY
	case needDex > dex:
		return dexBtnX, dexBtnY
	}
	return vitBtnX, vitBtnY
}

// spendNext is Clean's choice once the screen is clear: the stat door while
// points are banked and the belief stands, then the skill door, then done.
func spendNext(statPts int, statsWork bool, skillPts int, skillsWork bool) spPhase {
	switch {
	case statPts > 0 && statsWork:
		return spStatDoor
	case skillPts > 0 && skillsWork:
		return spSkillDoor
	}
	return spClean // nothing left: Done
}

// planB: the frozen-click answer. The FIRST frozen click may press the
// door's hotkey ('C'/'T', farmbot-proven) — but only when the Reading
// positively shows the panel NOT up: the hotkey TOGGLES, so pressed on an open
// panel it closes it, and pressed blind it is how 2026-09-24's Spend typed 'C'
// into Drognan's trade window. frozen≥3 retires the belief.
type planB uint8

const (
	planRetry  planB = iota // click again
	planHotkey              // press the door hotkey once
	planRetire              // frozen through both doors
)

func spendPlanB(frozen int, sighted, panelUp bool) planB {
	switch {
	case frozen >= 3:
		return planRetire
	case frozen == 1 && sighted && !panelUp:
		return planHotkey
	}
	return planRetry
}

// Spend — P-8 POINTS ARE ORDNANCE. Banked stat points buy the wardrobe's
// requirement gates first (P-8.3: exactly to the gate, strength before
// dexterity; the owner's unique bow is the standing customer), the rest go to
// vitality. The panel opens by its SCREEN BUTTON (P-8.2 — WARNING 5, panel
// hotkeys are deaf); every click is judged by the StatPoints delta (SPEND,
// Lexicon). No ESC: the sheet and tree close by sight (Close phase or the
// janitor), from a clean screen that Clean guaranteed.
type Spend struct {
	life       svcLife[spPhase]
	verified   int // clicks proven by a points delta since the panel opened
	frozen     int // consecutive clicks with no delta
	judging    bool
	before     int
	skVerified int
	skFrozen   int
	skStage    uint8 // Skills: 0 tab next, 1 seat next, 2 judging
}

var (
	_ Life   = (*Spend)(nil)
	_ Phased = (*Spend)(nil)
)

func NewSpend() *Spend {
	sp := &Spend{}
	sp.life.init(sp.Name(), spClose, spendClaims)
	sp.life.ph.Budget(spClean, 10*time.Second)
	sp.life.ph.Budget(spStatDoor, 3*time.Second)
	sp.life.ph.Budget(spStats, 45*time.Second)
	sp.life.ph.Budget(spSkillDoor, 3*time.Second)
	sp.life.ph.Budget(spSkills, 45*time.Second)
	sp.life.ph.Budget(spClose, 8*time.Second)
	return sp
}

func (sp *Spend) Name() string      { return "spend" }
func (sp *Spend) PhaseName() string { return sp.life.ph.Phase().String() }

func (sp *Spend) Demand(s *percept.Snapshot) *arbiter.Demand { return sp.life.keepBid(sp.demand(s), s) }

func (sp *Spend) demand(s *percept.Snapshot) *arbiter.Demand {
	if servicesCooled() {
		return nil
	}
	if !s.Valid || !s.Me.InTown {
		return nil
	}
	wantStats := s.Me.StatPoints > 0 && spendWorks.Load()
	wantSkills := s.Me.SkillPoints > 0 && skillSpendWorks.Load()
	if !wantStats && !wantSkills {
		return nil
	}
	urg := 0.35 // vitality dump: below Identify — the docket may still reveal a gate
	if wantSkills {
		urg = 0.6 // P-8.7: skill points ARE the dps — above the vitality dump
	}
	if wantStats && (s.Me.NeedStr > s.Me.Str || s.Me.NeedDex > s.Me.Dex) {
		urg = 0.8 // a gate is buyable: clear it BEFORE Equip (0.75) runs this visit (P-8.1)
	}
	return &arbiter.Demand{Who: sp.Name(), Class: arbiter.ClassService,
		Urgency: urg,
		Commit:  arbiter.Commitment{MinHold: 4 * time.Second}}
}

func (sp *Spend) Needs(*percept.Snapshot) Needs { return sp.life.needs() }

func (sp *Spend) Begin(ctx *Ctx, resumed bool) {
	sp.life.begin(resumed)
	sp.judging, sp.skStage = false, 0
	if !resumed {
		sp.verified, sp.frozen, sp.skVerified, sp.skFrozen = 0, 0, 0, 0
		return
	}
	// A preempted Spend re-verifies from a clean screen: with the janitor ON
	// its sheet/tree was closed the moment the grant was lost; with it OFF
	// Clean closes what is left by sight. Both doors reopen from Clean.
	if cur := sp.life.ph.Phase(); cur != spClean && cur != spClose {
		sp.verified, sp.frozen, sp.skVerified, sp.skFrozen = 0, 0, 0, 0
		sp.life.to(spClean, "resumed: "+cur.String()+" re-verifies from a clean screen")
	}
}

func (sp *Spend) Suspend(ctx *Ctx, _ phase.Reason) { sp.life.suspend(ctx) }

func (sp *Spend) End(ctx *Ctx, v phase.Verdict, why phase.Reason) {
	sp.life.end(ctx, v, why)
	sp.verified, sp.frozen, sp.skVerified, sp.skFrozen, sp.judging, sp.skStage = 0, 0, 0, 0, false, 0
}

func (sp *Spend) Step(ctx *Ctx) Status {
	l := &sp.life
	if st, over := l.overrun(ctx); over {
		return st
	}
	if l.ph.Phase() == spClose {
		return l.closing(ctx)
	}
	s := ctx.Snap
	if !s.Valid {
		return l.wait(100 * time.Millisecond)
	}
	if s.Me.CursorItem {
		return l.wait(150 * time.Millisecond) // WARNING 9: no clicks while an item rides the cursor
	}
	switch l.ph.Phase() {
	case spClean:
		if ok, st := l.cleanScreen(ctx, 0); !ok {
			return st
		}
		next := spendNext(s.Me.StatPoints, spendWorks.Load(), s.Me.SkillPoints, skillSpendWorks.Load())
		if next == spClean {
			return l.finish(ctx, phase.Done, phase.Completed, "all ordnance spent or retired")
		}
		l.to(next, "screen clear")
		return l.running()
	case spStatDoor:
		return sp.statDoor(ctx)
	case spStats:
		return sp.stats(ctx, s)
	case spSkillDoor:
		return sp.skillDoor(ctx)
	case spSkills:
		return sp.skills(ctx, s)
	}
	return l.running()
}

// interlock: the SAFETY INTERLOCK (Equip's law) on the Reading, not on the
// memory NPCShop flag that read false with Drognan's window up (2026-09-24):
// panel clicks with a vendor, an NPC menu or the bag up are never made —
// back to Clean, which closes them first.
func (sp *Spend) interlock(ctx *Ctx) (Status, bool) {
	if f, bad := sp.life.interlocked(ctx); bad {
		sp.judging, sp.skStage = false, 0
		sp.life.to(spClean, "interlock: "+f.String()+" up — no click into it")
		return sp.life.running(), true
	}
	return Status{}, false
}

func (sp *Spend) statDoor(ctx *Ctx) Status {
	l := &sp.life
	if st, stop := sp.interlock(ctx); stop {
		return st
	}
	if seen, _ := seenPanels(ctx); seen&claimsSheet != 0 {
		l.to(spStats, "char sheet already up")
		return l.running()
	}
	ctx.M.MoveStop()
	snapPNG(ctx, "logs/spend_pre.png") // is the New Stats button even there?
	ctx.M.UIClick(newStatsBtnX, newStatsBtnY)
	l.to(spStats, "New Stats clicked")
	// SPEND (Lexicon): door settle 1 s — the panel's slide-in eats early clicks
	// (measured 08:41: two clicks into the animation retired the whole belief).
	return l.wait(1100 * time.Millisecond)
}

func (sp *Spend) stats(ctx *Ctx, s *percept.Snapshot) Status {
	l := &sp.life
	if sp.judging {
		return sp.judgeStat(ctx)
	}
	if s.Me.StatPoints <= 0 || !spendWorks.Load() {
		// Stats spent: back to Clean, which closes the sheet (it is foreign
		// there) before the skill door — never a toggle, never an ESC.
		l.to(spClean, fmt.Sprintf("stats done (verified %d)", sp.verified))
		return l.running()
	}
	if st, stop := sp.interlock(ctx); stop {
		return st
	}
	if sp.frozen == 0 && sp.verified == 0 {
		snapPNG(ctx, "logs/spend_door.png") // did the New Stats click open anything?
	}
	bx, by := statRecipient(s.Me.Str, s.Me.NeedStr, s.Me.Dex, s.Me.NeedDex)
	sp.before = s.Me.StatPoints
	ctx.M.UIClick(bx, by)
	sp.judging = true
	return l.wait(300 * time.Millisecond)
}

func (sp *Spend) judgeStat(ctx *Ctx) Status {
	l := &sp.life
	sp.judging = false
	if sp.frozen == 1 && sp.verified == 0 {
		snapPNG(ctx, "logs/spend_click.png") // where did the plus click actually land?
	}
	after := sp.before
	if v, ok := ctx.GR.GetData().PlayerUnit.BaseStats.FindStat(stat.StatPoints, 0); ok {
		after = v.Value
	}
	if after < sp.before {
		sp.verified++
		sp.frozen = 0
		return l.wait(450 * time.Millisecond) // let the last click land before the next
	}
	sp.frozen++
	seen, sighted := seenPanels(ctx)
	switch spendPlanB(sp.frozen, sighted, seen&claimsSheet != 0) {
	case planHotkey:
		// PLAN B DOOR after the first frozen click: the New Stats button comes
		// and goes (photographed present 08:21, absent 09:35) — but farmbot's
		// -statalloc PROVED the 'c' hotkey opens the char panel on this build,
		// BaseStats-delta-verified. Only with the sheet SEEN closed.
		ctx.M.PressKey(0x43) // 'C'
		ctx.Led.Append(verbs.Outcome{Verb: "spend", Holder: sp.Name(), Result: verbs.ResWhiff,
			Evidence: "plus click frozen and the sheet is not up — plan B door 'C'"})
		return l.wait(1100 * time.Millisecond)
	case planRetire:
		// Frozen through both doors: retire for the session (SPEND, Lexicon).
		spendWorks.Store(false)
		ev := fmt.Sprintf("count frozen at %d through both doors (verified %d) — belief retired", sp.before, sp.verified)
		ctx.Led.Append(verbs.Outcome{Verb: "spend", Holder: sp.Name(), Result: verbs.ResDeaf, Evidence: ev})
		return l.finish(ctx, phase.Abandoned, phase.Deaf, ev)
	}
	return l.wait(450 * time.Millisecond)
}

func (sp *Spend) skillDoor(ctx *Ctx) Status {
	l := &sp.life
	// RETIRE, don't just stop: a can't-spend verdict with SkillPoints>0 would
	// re-bid the same instant — 88k grants/min hot-spin, measured 12:32. The
	// belief is silenced for the session.
	if ctx.Cap == nil || ctx.Cap.Reach == nil {
		skillSpendWorks.Store(false)
		ctx.Led.Append(verbs.Outcome{Verb: "spend", Holder: sp.Name(), Result: verbs.ResRefused,
			Evidence: "no proven REACH TOOL — skill points banked (P-8.5)"})
		return l.finish(ctx, phase.Abandoned, phase.Precondition, "no proven reach tool")
	}
	sk := ctx.Cap.Reach.Skill
	desc := sk.Desc()
	if desc.Page < 1 || desc.Page > 3 || desc.Row < 1 || desc.Row > 6 || desc.Column < 1 || desc.Column > 3 {
		skillSpendWorks.Store(false)
		ev := fmt.Sprintf("skill %d has no tree seat (page/row/col) — banked, never click blind", int(sk))
		ctx.Led.Append(verbs.Outcome{Verb: "spend", Holder: sp.Name(), Result: verbs.ResRefused, Evidence: ev})
		return l.finish(ctx, phase.Abandoned, phase.Precondition, ev)
	}
	if st, stop := sp.interlock(ctx); stop {
		return st
	}
	if seen, _ := seenPanels(ctx); seen&claimsTree != 0 {
		l.to(spSkills, "skill tree already up")
		return l.running()
	}
	ctx.M.MoveStop()
	// The NEW SKILL button is the primary door (photographed live at client
	// (755,661), 12:12 — P-8.2: screen buttons over hotkeys); farmbot's 'T'
	// toggle waits as plan B on the first frozen read.
	ctx.M.UIClick(newSkillBtnX, newSkillBtnY)
	l.to(spSkills, "New Skill clicked")
	return l.wait(700 * time.Millisecond)
}

// skills — P-8.7: one skill point per knock into the proven REACH TOOL's
// skill, tree seat from the skill's own Desc against farmbot's proven grid.
// Judged by the SkillPoints delta.
func (sp *Spend) skills(ctx *Ctx, s *percept.Snapshot) Status {
	l := &sp.life
	if sp.skStage == 0 && (s.Me.SkillPoints <= 0 || !skillSpendWorks.Load()) {
		l.to(spClean, fmt.Sprintf("skills done (verified %d)", sp.skVerified))
		return l.running()
	}
	if ctx.Cap == nil || ctx.Cap.Reach == nil {
		l.to(spSkillDoor, "reach tool lost")
		return l.running()
	}
	desc := ctx.Cap.Reach.Skill.Desc()
	switch sp.skStage {
	case 0:
		if st, stop := sp.interlock(ctx); stop {
			return st
		}
		if sp.skVerified == 0 && sp.skFrozen == 0 {
			snapPNG(ctx, "logs/skilltree.png") // first knock: the door photographs itself
		}
		sp.before = s.Me.SkillPoints
		ctx.M.UIClick(skillTabX[desc.Page], skillTabY)
		sp.skStage = 1
		return l.wait(250 * time.Millisecond)
	case 1:
		ctx.M.UIClick(skillColX[desc.Column], skillRowY[desc.Row])
		sp.skStage = 2
		return l.wait(350 * time.Millisecond)
	}
	sp.skStage = 0
	after := sp.before
	if v, ok := ctx.GR.GetData().PlayerUnit.BaseStats.FindStat(stat.SkillPoints, 0); ok {
		after = v.Value
	}
	if after < sp.before {
		sp.skVerified++
		sp.skFrozen = 0
		return l.wait(700 * time.Millisecond)
	}
	sp.skFrozen++
	seen, sighted := seenPanels(ctx)
	switch spendPlanB(sp.skFrozen, sighted, seen&claimsTree != 0) {
	case planHotkey:
		ctx.M.PressKey(0x54) // plan B: farmbot's 'T' toggle — only with the tree SEEN closed
		ctx.Led.Append(verbs.Outcome{Verb: "spend", Holder: sp.Name(), Result: verbs.ResWhiff,
			Evidence: "skill knock frozen and the tree is not up — plan B door 'T'"})
	case planRetire:
		skillSpendWorks.Store(false)
		ev := fmt.Sprintf("skill points frozen at %d through both doors (verified %d) — tree belief retired", sp.before, sp.skVerified)
		ctx.Led.Append(verbs.Outcome{Verb: "spend", Holder: sp.Name(), Result: verbs.ResDeaf, Evidence: ev})
		return l.finish(ctx, phase.Abandoned, phase.Deaf, ev)
	}
	return l.wait(700 * time.Millisecond)
}
