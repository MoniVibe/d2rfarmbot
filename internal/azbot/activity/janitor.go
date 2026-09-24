package activity

import (
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

// JanitorOn: the executive's janitor + gate is live (AZBOT_JANITOR=1 or
// -janitor; docs/AZBOT_V2.md step 6). Set once by main before the loop. When
// true, activities send no ESC to close their own panels: losing the grant
// drops their Claims and the janitor closes what is left by sight. When false
// every legacy closer runs exactly as before.
var JanitorOn bool

// disowned: panels the CURRENT holder has given up while still holding the
// grant — its Claims minus these are what the gate protects. A legacy closer
// that used to ESC its own panel mid-episode (a stray NPC menu, the char sheet
// before the skill tree) disowns it instead, and the janitor closes it by
// sight on the next tick. The executive clears the set when the holder changes.
var disowned struct {
	p  screen.Panel
	at time.Time
}

// Disown hands p to the janitor for the rest of this holder's grant.
func Disown(p screen.Panel) {
	disowned.p |= p
	disowned.at = time.Now()
}

// Disowned is the holder's disowned panel set.
func Disowned() screen.Panel { return disowned.p }

// ClearDisowned forgets every disowned panel (a new holder, or a new episode).
func ClearDisowned() { disowned.p = 0 }

// SettleDisowned keeps only the disowned panels still present on screen, once
// the disown is old enough for the screen belief to have caught up — so a panel
// the activity opens again later in the same grant is its own again.
func SettleDisowned(present screen.Panel, now time.Time) {
	if disowned.p != 0 && now.Sub(disowned.at) >= 300*time.Millisecond {
		disowned.p &= present
	}
}
