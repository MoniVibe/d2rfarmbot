package screen

import "image"

// ---------------------------------------------------------------- Unphotographed

// NO CAPTURE, NO COORDINATE. A detector without a real 1920x1050 shot is a
// guess that lies with confidence, so it answers (seen=false, unsure=true):
// absence is NOT proven. Relay R2 photographed every panel but one.

// sightStub is the shared "detector unavailable" answer.
func sightStub(img image.Image) (seen, unsure bool) { return false, true }

// CursorItemVisible: no capture of an item riding the cursor exists yet.
// Memory's cursor read (WARNING 9, Hints.CursorItem) is the proven channel
// meanwhile. TODO(capture relay/R3/cursor_item.png).
func CursorItemVisible(img image.Image) (seen, unsure bool) { return sightStub(img) }
