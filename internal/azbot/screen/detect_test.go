package screen

import (
	"image"
	"image/png"
	"os"
	"sync"
	"testing"
)

// The real 1920x1050 captures live beside the Windows-only game package; they
// are read in place, never copied.
const testdata = "../../game/testdata/"

var captures = []string{"chronicle", "loot_filter", "npc_menu", "pause_menu",
	"shop_armor", "shop_open", "town_after_trade", "town_clear"}

var (
	capMu    sync.Mutex
	capCache = map[string]image.Image{}
)

func load(t *testing.T, name string) image.Image {
	t.Helper()
	capMu.Lock()
	defer capMu.Unlock()
	if img, ok := capCache[name]; ok {
		return img
	}
	f, err := os.Open(testdata + name + ".png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	capCache[name] = img
	return img
}

// Real 1920x1050 captures from the MSI, 2026-09-23: the pause menu must be seen,
// a clear town and an open shop must not.
func TestPauseMenuVisible(t *testing.T) {
	for file, want := range map[string]bool{
		"pause_menu": true, "town_clear": false, "shop_open": false,
		"chronicle": false, "loot_filter": false, "npc_menu": false, "town_after_trade": false,
	} {
		if got := PauseMenuVisible(load(t, file)); got != want {
			t.Errorf("%s: PauseMenuVisible=%v, want %v", file, got, want)
		}
	}
}

func TestTradePanelVisible(t *testing.T) {
	for file, want := range map[string]bool{
		"shop_open": true, "shop_armor": true, "town_clear": false,
		"pause_menu": false, "npc_menu": false, "town_after_trade": false,
		"chronicle": false, "loot_filter": false, // dark sub-panel interior over a dimmed town
	} {
		if got := TradePanelVisible(load(t, file)); got != want {
			t.Errorf("%s: TradePanelVisible=%v, want %v", file, got, want)
		}
	}
}

func TestUIBlockerAndShopX(t *testing.T) {
	for file, want := range map[string]string{
		"loot_filter": "subpanel", "chronicle": "subpanel",
		"pause_menu": "pause", "town_clear": "",
		"shop_open": "", "npc_menu": "", "town_after_trade": "",
	} {
		if kind, _, _, _ := UIBlocker(load(t, file)); kind != want {
			t.Errorf("%s: UIBlocker=%q, want %q", file, kind, want)
		}
	}
	for file, want := range map[string]bool{
		"shop_open": true, "shop_armor": true,
		"town_clear": false, "pause_menu": false, "loot_filter": false, "town_after_trade": false,
	} {
		if _, _, got := ShopOpenX(load(t, file)); got != want {
			t.Errorf("%s: ShopOpenX=%v, want %v", file, got, want)
		}
	}
	if x, y := ReturnToGameAt(load(t, "pause_menu")); x != 960 || y != 685 {
		t.Errorf("ReturnToGameAt=(%d,%d), want (960,685) at reference size", x, y)
	}
}

// The half-panel frames: fire on both shop captures (vendor left, bag right),
// ZERO false positives on everything else — the clear towns above all.
func TestHalfPanels(t *testing.T) {
	shop := map[string]bool{"shop_open": true, "shop_armor": true}
	for _, file := range captures {
		img := load(t, file)
		ls, rs := LeftPanelScore(img), RightPanelScore(img)
		t.Logf("%-17s left in=%.2f/%.2f out=%.2f/%.2f  right in=%.2f/%.2f out=%.2f/%.2f",
			file, ls.In[0], ls.In[1], ls.Out[0], ls.Out[1], rs.In[0], rs.In[1], rs.Out[0], rs.Out[1])
		if got := ls.Present(); got != shop[file] {
			t.Errorf("%s: LeftPanelVisible=%v, want %v", file, got, shop[file])
		}
		if got := rs.Present(); got != shop[file] {
			t.Errorf("%s: RightPanelVisible=%v, want %v", file, got, shop[file])
		}
		if _, _, got := RightPanelX(img); got != shop[file] {
			t.Errorf("%s: RightPanelX=%v, want %v", file, got, shop[file])
		}
	}
	// Relay R2: the generic frames on the new captures. Captures with nothing
	// on a side must never read a frame there (dark Act 1 grass, the night
	// field, the dimmed pause menu); the named panels may (they are framed).
	leftSide := Shop | CharSheet | QuestLog | Waypoint | Mercenary | Stash
	rightSide := Inventory | SkillTree
	for _, n := range r2 {
		img := loadAny(t, "r2/"+n, false)
		ls, rs := LeftPanelScore(img), RightPanelScore(img)
		t.Logf("r2/%-28s left in=%.2f/%.2f out=%.2f/%.2f %-5v  right in=%.2f/%.2f out=%.2f/%.2f %v",
			n, ls.In[0], ls.In[1], ls.Out[0], ls.Out[1], ls.Present(), rs.In[0], rs.In[1], rs.Out[0], rs.Out[1], rs.Present())
		if truth["r2/"+n]&leftSide == 0 && ls.Present() {
			t.Errorf("r2/%s: left frame with nothing on the left", n)
		}
		if truth["r2/"+n]&rightSide == 0 && rs.Present() {
			t.Errorf("r2/%s: right frame with nothing on the right", n)
		}
	}
	if x, y, _ := RightPanelX(load(t, "shop_open")); x != 1788 || y != 18 {
		t.Errorf("RightPanelX=(%d,%d), want (1788,18) at reference size", x, y)
	}
}

func TestDetectorsRejectNilAndTiny(t *testing.T) {
	tiny := image.NewRGBA(image.Rect(0, 0, 100, 100))
	for _, img := range []image.Image{nil, tiny} {
		if PauseMenuVisible(img) || TradePanelVisible(img) || ShopVisible(img) ||
			LeftPanelVisible(img) || RightPanelVisible(img) {
			t.Errorf("detector fired on %v", img)
		}
		if _, _, _, ok := UIBlocker(img); ok {
			t.Error("UIBlocker fired on unusable image")
		}
	}
}

// Anchoring: a half-height capture maps the reference points proportionally.
func TestScaling(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 960, 525))
	if x, y := ScaleShot(img, 960, 685); x != 480 || y != 342 {
		t.Errorf("ScaleShot=(%d,%d)", x, y)
	}
	if x, y := fromLeft(img, 657, 120); x != 328 || y != 60 {
		t.Errorf("fromLeft=(%d,%d)", x, y)
	}
	if x, y := fromRight(img, 1788, 18); x != 894 || y != 9 {
		t.Errorf("fromRight=(%d,%d)", x, y)
	}
}
