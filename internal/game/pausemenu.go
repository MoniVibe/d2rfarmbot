package game

import (
	"image"

	"github.com/hectorgimenez/koolo/internal/azbot/screen"
)

// The screen detectors live in internal/azbot/screen (pure: tested on Linux
// against testdata/ here). These wrappers keep the existing call sites; the
// measurements and their history are documented there.

// ReturnToGameShot is the "Return to Game" button center on a 1920x1050 capture.
var ReturnToGameShot = screen.ReturnToGameShot

// PauseMenuVisible reports whether the ESC/pause menu is on screen.
func PauseMenuVisible(img image.Image) bool { return screen.PauseMenuVisible(img) }

// ReturnToGameAt: the Return-to-Game button in img's pixel space.
func ReturnToGameAt(img image.Image) (int, int) { return screen.ReturnToGameAt(img) }

// TradePanelVisible: an empty stock grid on a proven vendor panel (never the shop proof).
func TradePanelVisible(img image.Image) bool { return screen.TradePanelVisible(img) }

// UIBlocker: a pause menu or sub-panel and where to click it away (never ESC).
func UIBlocker(img image.Image) (kind string, x, y int, ok bool) { return screen.UIBlocker(img) }

// ShopOpenX reports the vendor panel's red close X.
func ShopOpenX(img image.Image) (int, int, bool) { return screen.ShopOpenX(img) }

// ShopVisible: the vendor panel is on screen (red X inside the vendor chrome).
func ShopVisible(img image.Image) bool { return screen.ShopVisible(img) }
