// Package trace formats the executive's observability lines: one "T" line per
// state transition at every layer, one "S" state line per second, and the
// flight recorder's decision frame. Pure formatting — callers own the sink.
//
// Grep contract (the laptop's log readers depend on it):
//
//	T L=grant from=fight to=flee why="preempt: survive > fight" tick=48213
//	T L=ui from=world to=world+inventory why="right-half panel (...)" tick=48213 hold=fight
//	T L=phase act=fence from=Seek to=Approach why="npc loaded" inPhase=1.2s tick=48213
//	T L=nav act=advance from=following to=stuck why="no gain 1.5s" tick=48213
//	T L=life act=fight ev=end why="abandoned/no-target: withdrawn while suspended" tick=48213
//	T L=gate hold=fight why="foreign: right-panel" act="click 1788,18" tick=48213
//	S 15:04:05 #48213 ses=InGame mode=World ui=- cur=- hold=fight held=7.2s gate=- phase=- hp=64 mp=30 pos=5012,4431 en=5
package trace

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/arbiter"
	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Grant is a holder change straight from the arbiter.
func Grant(ch arbiter.Change, tick uint64) string {
	return fmt.Sprintf("T L=grant from=%s to=%s why=%q tick=%d", dash(ch.From), dash(ch.To), ch.Why(), tick)
}

// EndWhy is "verdict/reason[: detail]" — the arbiter's ended-why and the log's.
func EndWhy(v phase.Verdict, why phase.Reason, detail string) string {
	s := v.String() + "/" + why.String()
	if detail != "" {
		s += ": " + detail
	}
	return s
}

// Ended is the holder's episode closed by the executive (verdict, monitor, stall).
func Ended(who string, v phase.Verdict, why phase.Reason, detail string, tick uint64) string {
	return fmt.Sprintf("T L=grant from=%s to=- why=%q tick=%d", dash(who), "end: "+EndWhy(v, why, detail), tick)
}

// Life is a lifecycle event that is not a grant change (a suspended activity's
// episode closing because it stopped bidding).
func Life(act, ev, why string, tick uint64) string {
	return fmt.Sprintf("T L=life act=%s ev=%s why=%q tick=%d", dash(act), ev, why, tick)
}

// UI is a believed screen change from screen.Tracker.
func UI(t screen.Transition, tick uint64, hold string) string {
	return fmt.Sprintf("T L=ui from=%s to=%s why=%q tick=%d hold=%s", t.From, t.To, t.Evidence, tick, dash(hold))
}

// Gate is one janitor judgment worth a line: an action taken ("click 1788,18",
// "key 0x1B", "drop 960,665"), or a change in why the holder is held ("-").
func Gate(hold, why, act string, tick uint64) string {
	return fmt.Sprintf("T L=gate hold=%s why=%q act=%q tick=%d", dash(hold), why, dash(act), tick)
}

// Phase re-heads a phase.Phaser log line ("phase act=... from=... to=...").
func Phase(line string, tick uint64) string {
	return fmt.Sprintf("T L=phase %s tick=%d", strings.TrimPrefix(line, "phase "), tick)
}

// Nav surfaces a nav follower transition that reached the ledger as verb "nav"
// with evidence "nav: <from> -> <to> (<why>)". ok is false for any other nav
// evidence (march notes, debug renders) — those are not transitions.
func Nav(act, evidence string, tick uint64) (string, bool) {
	rest, ok := strings.CutPrefix(evidence, "nav: ")
	if !ok {
		return "", false
	}
	from, rest, ok := strings.Cut(rest, " -> ")
	if !ok {
		return "", false
	}
	to, why := rest, ""
	if i := strings.Index(rest, " ("); i >= 0 && strings.HasSuffix(rest, ")") {
		to, why = rest[:i], rest[i+2:len(rest)-1]
	}
	return fmt.Sprintf("T L=nav act=%s from=%s to=%s why=%q tick=%d", dash(act), from, to, why, tick), true
}

// ModeName is the state line's spelling of a screen mode.
func ModeName(m screen.Mode) string {
	switch m {
	case screen.World:
		return "World"
	case screen.Loading:
		return "Loading"
	case screen.Dead:
		return "Dead"
	}
	return "Unknown"
}

// UIName is the state line's panel set: "-" when nothing is up.
func UIName(p screen.Panel) string {
	if p == 0 {
		return "-"
	}
	return p.String()
}

// State is the 1 Hz state line. Empty strings print '-' (not wired yet, or no read).
type State struct {
	At      time.Time
	Tick    uint64
	Session string
	Mode    string
	UI      string
	Cursor  string
	Hold    string
	Held    time.Duration
	Gate    string
	Phase   string
	Valid   bool // snapshot fields (hp, mp, pos, en) are real
	HP, MP  int
	X, Y    int
	Enemies int
}

func (s State) String() string {
	held := "-"
	if s.Hold != "" {
		held = fmt.Sprintf("%.1fs", s.Held.Seconds())
	}
	hp, mp, pos, en := "-", "-", "-", "-"
	if s.Valid {
		hp, mp = fmt.Sprint(s.HP), fmt.Sprint(s.MP)
		pos, en = fmt.Sprintf("%d,%d", s.X, s.Y), fmt.Sprint(s.Enemies)
	}
	return fmt.Sprintf("S %s #%d ses=%s mode=%s ui=%s cur=%s hold=%s held=%s gate=%s phase=%s hp=%s mp=%s pos=%s en=%s",
		s.At.Format("15:04:05"), s.Tick, dash(s.Session), dash(s.Mode), dash(s.UI), dash(s.Cursor),
		dash(s.Hold), held, dash(s.Gate), dash(s.Phase), hp, mp, pos, en)
}

// FrameKind tags a decision frame in the flight file. Snapshot lines carry no
// "k" key, so old files read unchanged and replay skips frames by this tag.
const FrameKind = "decision"

// Frame is the flight recorder's decision record, written beside each 1 Hz
// snapshot line: who held, the last grant change and why, the holder's phase,
// and what the screen oracle believed.
type Frame struct {
	K          string    `json:"k"`
	Tick       uint64    `json:"tick"`
	At         time.Time `json:"at"`
	Seq        uint64    `json:"seq"` // the snapshot line this frame follows
	Holder     string    `json:"holder,omitempty"`
	Class      string    `json:"class,omitempty"`
	HeldS      float64   `json:"held_s,omitempty"`
	Change     string    `json:"change,omitempty"` // last non-keep change, "from -> to (why)"
	ChangeTick uint64    `json:"change_tick,omitempty"`
	Phase      string    `json:"phase,omitempty"`
	Screen     string    `json:"screen,omitempty"` // tracker belief
	Seen       string    `json:"seen,omitempty"`   // last raw reading (with unsure)
	Gate       string    `json:"gate,omitempty"`   // janitor gate: ok | blocked(<panels>) | wedge
	Bids       int       `json:"bids"`
}

// IsFrame reports whether a flight line is a decision frame, and decodes it.
func IsFrame(line []byte) (Frame, bool) {
	var probe struct {
		K string `json:"k"`
	}
	if json.Unmarshal(line, &probe) != nil || probe.K != FrameKind {
		return Frame{}, false
	}
	var f Frame
	if json.Unmarshal(line, &f) != nil {
		return Frame{}, false
	}
	return f, true
}
