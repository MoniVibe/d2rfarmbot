package main

// The executive's v2 observability and the screen oracle in SHADOW mode
// (docs/AZBOT_V2.md steps 4-5): trace lines, the 1 Hz state line, decision
// frames, and a bounded-rate screen read that is logged and published but
// never gates or acts.

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data/mode"
	"github.com/hectorgimenez/koolo/internal/azbot/activity"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
	"github.com/hectorgimenez/koolo/internal/azbot/trace"
	"github.com/hectorgimenez/koolo/internal/game"
)

// T/S lines go to stdout raw (greppable from column 0), one Write per line so
// they never tear against slog's records.
var traceMu sync.Mutex

func emit(line string) {
	traceMu.Lock()
	fmt.Fprintln(os.Stdout, line)
	traceMu.Unlock()
}

// curTick is the executive's loop counter, readable from the ledger sink and
// the phase sink (they fire from inside Steps).
var curTick atomic.Uint64

// uiVerbs: ledger verbs whose Do clicks or keys a panel — the next tick
// photographs the screen instead of waiting for the cadence.
var uiVerbs = map[string]bool{
	"buy": true, "fence": true, "equip": true, "spend": true, "identify": true,
	"repair": true, "heal": true, "errand": true, "waypoint": true, "relog": true,
}

// screenHints fills the oracle's memory hints from what the snapshot carries.
// Loading, NPCShop and the Waypoint flag are not in percept.Snapshot yet: they
// stay false (absence proves nothing to the oracle).
func screenHints(s *percept.Snapshot) screen.Hints {
	return screen.Hints{
		Valid:      s.Valid,
		Dead:       s.Valid && (s.Me.HPPct <= 0 || s.Me.Mode == mode.Dead || s.Me.Mode == mode.Death),
		HPPct:      s.Me.HPPct,
		InTown:     s.Me.InTown,
		MenuByte:   s.MenuOpen,
		CursorItem: s.Me.CursorItem,
	}
}

type shadow struct {
	logger *slog.Logger
	gr     *game.MemoryReader
	cad    exec.Cadence
	tr     *screen.Tracker
	Eye    *exec.Eye // the stable Reading, for the Sentinel (step 6)
	lat    exec.Latency
	latAt  time.Time
	kick   atomic.Bool
	seen   bool // at least one capture observed
	last   screen.Reading
	lineAt time.Time
}

// Every 2nd tick, 100ms floor: the 40ms tick floor would otherwise ask for
// 12 full-window captures a second.
func newShadow(logger *slog.Logger, gr *game.MemoryReader) *shadow {
	return &shadow{logger: logger, gr: gr, cad: exec.Cadence{EveryN: 2, MinGap: 100 * time.Millisecond},
		tr: screen.NewTracker(0), Eye: &exec.Eye{}, latAt: time.Now()}
}

// Kick: a panel was just clicked — photograph on the next tick.
func (sh *shadow) Kick() { sh.kick.Store(true) }

// observe captures and reads the screen when the cadence allows. Read-only:
// it logs, publishes, and measures — nothing downstream consults it yet.
func (sh *shadow) observe(tick uint64, s *percept.Snapshot, hold string) {
	now := time.Now()
	if sh.kick.Swap(false) {
		sh.cad.Kick()
	}
	if sh.cad.Due(tick, now) {
		t0 := time.Now()
		r := screen.Observe(sh.gr.Screenshot(), screenHints(s))
		sh.lat.Add(time.Since(t0))
		if tr, ok := sh.tr.Update(now, r); ok {
			emit(trace.UI(tr, tick, hold))
		}
		sh.last, sh.seen = r, true
		sh.Eye.Publish(exec.Seen{At: now, Tick: tick, State: sh.tr.State(), Reading: r})
	}
	if now.Sub(sh.latAt) >= time.Minute {
		p50, p99, max, n := sh.lat.Flush()
		sh.logger.Info("screen: capture cost (Screenshot+Observe)", "p50", p50.Round(100*time.Microsecond),
			"p99", p99.Round(100*time.Microsecond), "max", max.Round(100*time.Microsecond), "n", n,
			"perSec", fmt.Sprintf("%.1f", float64(n)/now.Sub(sh.latAt).Seconds()))
		sh.latAt = now
	}
}

// phaseOf: the holder's phase name, "" when it has none.
func phaseOf(roster *activity.Roster, who string) string {
	if p, ok := roster.Get(who).(activity.Phased); ok {
		return p.PhaseName()
	}
	return ""
}

func holderWho(a *arbiter.Arbiter) string {
	if g := a.Current(); g != nil {
		return g.Demand.Who
	}
	return ""
}

// stateLine prints the 1 Hz "S" line.
func (sh *shadow) stateLine(tick uint64, s *percept.Snapshot, ses string, arb *arbiter.Arbiter, roster *activity.Roster) {
	now := time.Now()
	if now.Sub(sh.lineAt) < time.Second {
		return
	}
	sh.lineAt = now
	hold := holderWho(arb)
	st := trace.State{At: now, Tick: tick, Session: ses, Hold: hold, Held: arb.Held(hold),
		Phase: phaseOf(roster, hold), Valid: s.Valid, HP: s.Me.HPPct, MP: s.Me.MPPct,
		X: s.Me.Pos.X, Y: s.Me.Pos.Y}
	if sh.seen {
		b := sh.tr.State()
		st.Mode, st.UI, st.Cursor = trace.ModeName(b.Mode), trace.UIName(b.Panels), "-"
		if b.CursorItem {
			st.Cursor = "item"
		}
	}
	for _, e := range s.Enemies {
		if !e.Walled {
			st.Enemies++
		}
	}
	emit(st.String())
}

// frame is the flight recorder's decision record for this second.
func (sh *shadow) frame(tick uint64, s *percept.Snapshot, arb *arbiter.Arbiter, core *exec.Core[*activity.Ctx],
	roster *activity.Roster, bids int) trace.Frame {
	f := trace.Frame{K: trace.FrameKind, Tick: tick, At: time.Now(), Seq: s.Seq, Bids: bids}
	if g := arb.Current(); g != nil {
		f.Holder, f.Class = g.Demand.Who, g.Demand.Class.String()
		f.HeldS = arb.Held(g.Demand.Who).Seconds()
		f.Phase = phaseOf(roster, g.Demand.Who)
	}
	if ch, at := core.Last(); ch.Changed() {
		f.Change, f.ChangeTick = ch.String(), at
	}
	if sh.seen {
		f.Screen, f.Seen = sh.tr.State().String(), sh.last.String()
	}
	return f
}
