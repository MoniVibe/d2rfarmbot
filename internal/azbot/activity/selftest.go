package activity

import (
	"fmt"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/game"
)

// ---------------------------------------------------------------- Preflight self-test
//
// PROVE THE PRIMITIVES IN TOWN, NOT IN THE FIELD (2026-09-23: a day of live
// trial-and-error discovered, one death at a time, that Act 2's shop had never
// been proven). Run in town, owner hands off, D2R focused. Harmless except for
// ONE healing-potion purchase, which is the proof of the whole trade chain.

// SelfTestResult is one primitive's verdict.
type SelfTestResult struct {
	Name     string
	Pass     bool
	Evidence string
}

func (r SelfTestResult) String() string {
	v := "FAIL"
	if r.Pass {
		v = "PASS"
	}
	return fmt.Sprintf("%-4s %-18s %s", v, r.Name, r.Evidence)
}

// SelfTest runs the preflight. refresh must return a fresh snapshot.
func SelfTest(ctx *Ctx, refresh func() *percept.Snapshot) []SelfTestResult {
	var out []SelfTestResult
	add := func(name string, pass bool, format string, a ...any) {
		out = append(out, SelfTestResult{name, pass, fmt.Sprintf(format, a...)})
	}
	snap := func() *percept.Snapshot { ctx.Snap = refresh(); return ctx.Snap }

	if n := EnsureWorld(ctx.GR, ctx.M); n > 0 {
		add("ui-cleared", true, "%d blocking screen(s) on screen at start — clicked away", n)
	}
	s := snap()
	add("focus", ctx.M.GameFocused(), "D2R foreground=%v (focused mode is the operating contract)", ctx.M.GameFocused())
	if !s.Valid || !s.Me.InTown {
		add("in-town", false, "valid=%v town=%v area=%d — self-test only runs in town", s.Valid, s.Me.InTown, int(s.Me.Area))
		return out
	}
	add("in-town", true, "area=%d pos=(%d,%d)", int(s.Me.Area), s.Me.Pos.X, s.Me.Pos.Y)

	// Belt: every occupied cell classified; the drink lanes exist.
	unknown := 0
	for _, b := range s.Me.BeltItems {
		if b.Kind == "unknown" {
			unknown++
		}
	}
	add("belt", unknown == 0, "used=%d/%d hp=%d mana=%d unknown=%d hpCols=%v",
		s.Me.BeltUsed, s.Me.BeltSlots, s.Me.BeltHP, s.Me.BeltMana, unknown, s.Me.HPCols)

	// World aim: hover-confirm the nearest town NPC (no click).
	d := ctx.GR.GetData()
	var npcU data.Monster
	best := 1 << 30
	for _, m := range d.Monsters {
		if !m.IsGoodNPC() {
			continue
		}
		if dd := chebyshev(s.Me.Pos, m.Position); dd < best && dd <= 18 {
			npcU, best = m, dd
		}
	}
	if best == 1<<30 {
		add("aim-npc", false, "no town NPC within 18 tiles to aim at")
	} else {
		me := d.PlayerUnit.Position
		bx := int(float32((npcU.Position.X-me.X)-(npcU.Position.Y-me.Y))*19.8) + ctx.GR.GameAreaSizeX/2
		by := int(float32((npcU.Position.X-me.X)+(npcU.Position.Y-me.Y))*9.9) + ctx.GR.GameAreaSizeY/2 + game.UnitAimDY()
		hit, probes := false, 0
	sweep:
		for dy := 0; dy >= -48; dy -= 12 {
			for _, dx := range []int{0, -10, 10} {
				probes++
				ctx.M.AimPhysical(bx+dx, by+dy+24)
				time.Sleep(50 * time.Millisecond)
				if hd := ctx.GR.GetData().HoverData; hd.IsHovered && hd.UnitID == npcU.UnitID {
					hit = true
					break sweep
				}
			}
		}
		add("aim-npc", hit, "npc=%d dist=%d confirmed=%v after %d probes", int(npcU.Name), best, hit, probes)
	}

	// Trade chain: talk → Trade → buy one healing potion → verified → close.
	if s.Me.Gold < 200 {
		add("trade", false, "gold=%d — cannot prove a purchase", s.Me.Gold)
		return out
	}
	r := NewRestock()
	open := false
	runUntil(75*time.Second, func() (bool, time.Duration) {
		snap()
		es := r.e.step(ctx, "selftest")
		open = es.open
		return es.open || es.dead, maxDur(es.wait, 150*time.Millisecond)
	})
	if !open {
		add("trade-open", false, "errand never reached an open trade window in 75s (phase=%s menuTry=%d)", r.e.ph.Phase(), r.e.menuTry)
		return out
	}
	add("trade-open", true, "vendor npc=%d stock=%d", int(r.e.npcID), len(vendorStock(ctx, func(data.Item) bool { return true })))
	want := func(it data.Item) bool { return percept.PotionKind(it, true) == "health" }
	if len(vendorStock(ctx, func(it data.Item) bool { return int(it.ID) == 603 })) > 0 {
		want = func(it data.Item) bool { return int(it.ID) == 603 }
	}
	var tx buyTx
	v := buyNoConfirm
	runUntil(10*time.Second, func() (bool, time.Duration) {
		vv, done, w := tx.step(ctx, "selftest", "health potion", want)
		v = vv
		return done, w
	})
	ev := ""
	if rec := ctx.Led.Recent(1); len(rec) > 0 {
		ev = rec[0].Evidence
	}
	if v != buyOK {
		snapPNG(ctx, "logs/selftest_buyfail.png") // the shop as it stood when the buy failed
	}
	add("buy-potion", v == buyOK, "%s", ev)
	runUntil(5*time.Second, func() (bool, time.Duration) { return closeShopStep(ctx) })
	return out
}

// runUntil drives a Step-shaped function (done, wait) to completion or the
// deadline — the manual harness's stand-in for the executive's Wait.
func runUntil(limit time.Duration, step func() (done bool, wait time.Duration)) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		done, wait := step()
		if done {
			return
		}
		time.Sleep(wait)
	}
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
