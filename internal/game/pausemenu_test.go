package game

import (
	"image"
	"image/png"
	"os"
	"testing"
)

// Real 1920x1050 captures from the MSI, 2026-09-23: the pause menu must be seen,
// a clear town and an open shop must not.
func TestPauseMenuVisible(t *testing.T) {
	for file, want := range map[string]bool{
		"testdata/pause_menu.png": true,
		"testdata/town_clear.png": false,
		"testdata/shop_open.png":  false,
	} {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := PauseMenuVisible(img); got != want {
			t.Errorf("%s: PauseMenuVisible=%v, want %v", file, got, want)
		}
	}
}

func TestTradePanelVisible(t *testing.T) {
	for file, want := range map[string]bool{
		"testdata/shop_open.png":        true,
		"testdata/shop_armor.png":       true,
		"testdata/town_clear.png":       false,
		"testdata/pause_menu.png":       false,
		"testdata/npc_menu.png":         false,
		"testdata/town_after_trade.png": false,
	} {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if got := TradePanelVisible(img); got != want {
			t.Errorf("%s: TradePanelVisible=%v, want %v", file, got, want)
		}
	}
}

func TestUIBlockerAndShopX(t *testing.T) {
	load := func(file string) image.Image {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		img, err := png.Decode(f)
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	for file, want := range map[string]string{
		"testdata/loot_filter.png": "subpanel", "testdata/chronicle.png": "subpanel",
		"testdata/pause_menu.png": "pause", "testdata/town_clear.png": "",
		"testdata/shop_open.png": "", "testdata/npc_menu.png": "",
	} {
		if kind, _, _, _ := UIBlocker(load(file)); kind != want {
			t.Errorf("%s: UIBlocker=%q, want %q", file, kind, want)
		}
	}
	for file, want := range map[string]bool{
		"testdata/shop_open.png": true, "testdata/shop_armor.png": true,
		"testdata/town_clear.png": false, "testdata/pause_menu.png": false, "testdata/loot_filter.png": false,
	} {
		if _, _, got := ShopOpenX(load(file)); got != want {
			t.Errorf("%s: ShopOpenX=%v, want %v", file, got, want)
		}
	}
}
