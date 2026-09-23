package main

// The executive's v2 observability (docs/AZBOT_V2.md step 4): trace lines,
// the 1 Hz state line, and decision frames.

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/activity"
	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/exec"
	"github.com/hectorgimenez/koolo/internal/azbot/percept"
	"github.com/hectorgimenez/koolo/internal/azbot/trace"
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

type shadow struct {
	lineAt time.Time
}

func newShadow(*slog.Logger) *shadow { return &shadow{} }

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
	return f
}
