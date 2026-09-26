package activity

// JanitorOn: the executive's janitor + gate is live (AZBOT_JANITOR=1 or
// -janitor; docs/AZBOT_V2.md step 6). Set once by main before the loop. When
// true, activities send no key to close their own panels: losing the grant —
// or moving to a phase that no longer claims a panel (servicelife.go) — makes
// it foreign, and the janitor closes it by sight. When false the town services
// close their own leftovers by sight (tidy) and every legacy closer runs as
// before.
var JanitorOn bool
