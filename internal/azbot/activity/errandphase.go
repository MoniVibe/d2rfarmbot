package activity

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/phase"
	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

// errandPhase is the NPC errand's state (was a bare int). Ordered: the
// services test "at or past the talk" with >=.
type errandPhase uint8

const (
	erClean    errandPhase = iota // precondition: nothing on screen — a leftover menu/shop/bag is closed first
	erSeek                        // walk the ring until the NPC loads
	erApproach                    // reach the 4..7 talk band
	erTalk                        // hover-confirm, bare click, wait for the menu byte
	erMenu                        // menu open: Home/Down/Enter toward Trade
	erOpening                     // Trade selected: the window populates (≤2s), else the next menu slot
	erAct                         // trade open (or the talk landed): the owner acts
	erClose                       // janitor OFF: our own panels closed by sight before the verdict
)

func (p errandPhase) String() string {
	switch p {
	case erClean:
		return "Clean"
	case erSeek:
		return "Seek"
	case erApproach:
		return "Approach"
	case erTalk:
		return "Talk"
	case erMenu:
		return "Menu"
	case erOpening:
		return "Opening"
	case erAct:
		return "Act"
	case erClose:
		return "Close"
	}
	return "?"
}

// errandClaims: nothing is ours until we click the NPC. Walking (Seek,
// Approach) claims nothing, so a menu a walk-click opened beside another NPC
// is foreign and the janitor closes it; from the talk on, the NPC menu and
// speech, the trade window (the vendor IS the left frame) and the bag beside
// it are the errand's.
func errandClaims(p errandPhase) screen.Panel {
	if p >= erTalk {
		return claimsVendor
	}
	return 0
}

// Held-time budgets per errand phase (overrun ⇒ Abandoned(Timebox)). The walk
// and the talk also share errandBudget: a Talk↔Approach ping-pong refills no
// phase budget forever.
var errandBudgets = map[errandPhase]time.Duration{
	erClean:    10 * time.Second,
	erSeek:     30 * time.Second,
	erApproach: 20 * time.Second,
	erTalk:     20 * time.Second,
	erMenu:     5 * time.Second,
	erOpening:  5 * time.Second,
	erAct:      90 * time.Second,
	erClose:    8 * time.Second,
}

// errandBudget: held time an errand may spend before its trade window opens
// (the old 45s wall-clock "service attempt" cap, now in held time).
const errandBudget = 45 * time.Second

// errandResume re-verifies the phase a suspended errand comes back to. Seek,
// Approach and Talk re-check themselves every Step. Menu needs the NPC menu
// still up and Opening/Act the trade window: with the janitor ON the executive
// closed them when the grant was lost (the claims dropped), and with it OFF
// they are trusted only if still SEEN — otherwise back to Clean, which closes
// what is left by sight and walks up to talk again.
func errandResume(p errandPhase, janitorOn, menuSeen, shopSeen bool) errandPhase {
	switch p {
	case erMenu:
		if !janitorOn && menuSeen {
			return erMenu
		}
		return erClean
	case erOpening, erAct:
		if !janitorOn && shopSeen {
			return p
		}
		return erClean
	}
	return p
}

// PhaseSink receives every phase.Phaser line (the executive traces them as
// "T L=phase"). nil = silent.
var PhaseSink func(line string)

func phaseLog(line string) {
	if PhaseSink != nil {
		PhaseSink(line)
	}
}

// PhaseName: the errand phase of each NPC service, for the state line.
func (r *Restock) PhaseName() string { return r.e.ph.Phase().String() }
func (fc *Fence) PhaseName() string  { return fc.e.ph.Phase().String() }
func (h *Heal) PhaseName() string    { return h.e.ph.Phase().String() }
func (rp *Repair) PhaseName() string { return rp.e.ph.Phase().String() }

// errandStep is one errand Step's answer to its owner service.
type errandStep struct {
	open bool          // the trade window is open: the owner acts
	dead bool          // give up this trip
	why  phase.Reason  // dead: why
	ev   string        // dead: evidence
	wait time.Duration // > 0: park this long (Status Wait) — never a sleep
}
