package screen

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------- Relay R2 fixtures
//
// THE 27 RELAY R2 CAPTURES AND THE R9 FRAME (branch relay-results,
// relay/R2/*.png and relay/R9/esc_loop_now.png, 1920x1050, ~2.7 MB each as
// PNG) ARE STORED AS TWO FILES EACH, ~0.25 MB together:
//
//	testdata/<name>.jpg      the full frame, JPEG q65 — the world behind the UI
//	testdata/<name>.roi.png  RGBA, lossless where alpha=255: EXACTLY the pixels
//	                         the fixed-position detectors read on that frame
//	                         (recorded while they ran on the original), plus the
//	                         NPC box on the captures that show one
//
// The loader decodes the JPEG into a 1920x1050 RGBA and pastes the lossless
// pixels back at their original coordinates, so every detector runs on a full
// frame and every sample it takes is the real pixel. Only the NPC-box scan
// (which sweeps the whole central band) reads JPEG pixels on the negatives —
// JPEG only blurs the gold rule it looks for, and TestMatrixOnOriginals runs
// the same matrix on the untouched PNGs when AZBOT_R2_DIR points at them.
//
// Regenerate (after changing a detector's sample points):
//
//	git archive origin/relay-results relay/R2 relay/R9 | tar -x -C /tmp/r2
//	AZBOT_R2_DIR=/tmp/r2/relay/R2 AZBOT_R9_DIR=/tmp/r2/relay/R9 AZBOT_WRITE_FIXTURES=1 \
//	  go test ./internal/azbot/screen -run TestWriteFixtures
const fixtures = "testdata/"

// r2 lists the relay R2 captures (README.md there maps each to its content).
var r2 = []string{"automap_overlay", "charsheet", "chat", "hireling", "inv_and_sheet", "inventory",
	"inventory_2", "inventory_act1", "inventory_act1_2", "npc_dialog_text", "npc_menu_2", "npc_menu_3",
	"npc_menu_4", "npc_talk", "pause_menu", "questlog", "skill_picker", "skill_picker_2", "skilltree",
	"stash", "waypoint", "waypoint_act1_and_inventory", "world_cave", "world_field_night", "world_town",
	"world_town_2", "world_town_act1"}

// r9: the relay R9 frame that stood while the janitor looped (2026-09-24,
// Maggot Lair level 1, mid-fight): the pause menu over the dimmed lair — the
// ESC the phantom shop earned raised it. The only panel on it is the pause
// menu; no shop, no sub-panel, no side panel may read there.
var r9 = []string{"esc_loop_now"}

// relayFixtures: every stored fixture as "<relay>/<name>".
func relayFixtures() []string {
	var out []string
	for _, n := range r2 {
		out = append(out, "r2/"+n)
	}
	for _, n := range r9 {
		out = append(out, "r9/"+n)
	}
	return out
}

// relayDir: the originals' directory for a fixture name ("" when unset).
func relayDir(name string) string {
	if strings.HasPrefix(name, "r9/") {
		return os.Getenv("AZBOT_R9_DIR")
	}
	return os.Getenv("AZBOT_R2_DIR")
}

// cutRelay splits "r2/<name>" / "r9/<name>".
func cutRelay(name string) (string, bool) {
	for _, pre := range []string{"r2/", "r9/"} {
		if n, ok := strings.CutPrefix(name, pre); ok {
			return n, true
		}
	}
	return name, false
}

// truth: what each capture shows, verified by eye (relay/R2/README.md, and the
// old captures above). The old shop shots had the automap up — the area name
// "…T GHOLEIN" stands beside the bag.
var truth = map[string]Panel{
	"chronicle": SubPanel, "loot_filter": SubPanel, "npc_menu": NPCMenu, "pause_menu": PauseMenu,
	"shop_armor": Shop | Inventory | Automap, "shop_open": Shop | Inventory | Automap,
	"town_after_trade": 0, "town_clear": 0,

	"r2/world_town": 0, "r2/world_town_2": 0, "r2/world_town_act1": 0, "r2/world_field_night": 0,
	"r2/inventory": Inventory, "r2/inventory_2": Inventory, "r2/inventory_act1": Inventory,
	"r2/inventory_act1_2": Inventory, "r2/charsheet": CharSheet, "r2/skilltree": SkillTree,
	"r2/inv_and_sheet": Inventory | CharSheet, "r2/questlog": QuestLog, "r2/automap_overlay": Automap,
	"r2/chat": Chat, "r2/skill_picker": SkillPicker, "r2/skill_picker_2": SkillPicker,
	"r2/hireling": Mercenary, "r2/npc_talk": NPCMenu, "r2/npc_dialog_text": NPCDialog,
	"r2/npc_menu_2": NPCMenu, "r2/npc_menu_3": NPCMenu, "r2/npc_menu_4": NPCMenu,
	"r2/stash": Stash | Inventory, "r2/waypoint": Waypoint, "r2/pause_menu": PauseMenu,
	"r2/waypoint_act1_and_inventory": Waypoint | Inventory,
	// Dark negatives: the Pit cave draws its area name top-right (the
	// automap's, non-blocking) and nothing else; the R9 lair frame is the
	// pause menu alone.
	"r2/world_cave": Automap, "r9/esc_loop_now": PauseMenu,
}

// allCaptures: old names first, then "r2/<name>", then "r9/<name>".
func allCaptures() []string {
	return append(append([]string(nil), captures...), relayFixtures()...)
}

// loadAny loads an old capture in place or an R2 fixture (or, with
// AZBOT_R2_DIR set and orig, the untouched R2 PNG).
func loadAny(t testing.TB, name string, orig bool) image.Image {
	t.Helper()
	n, ok := cutRelay(name)
	if !ok {
		return load(t.(*testing.T), name)
	}
	key := name
	if orig {
		key = "orig/" + name
	}
	capMu.Lock()
	defer capMu.Unlock()
	if img, ok := capCache[key]; ok {
		return img
	}
	var img image.Image
	if orig {
		img = decodeRGBA(t, filepath.Join(relayDir(name), n+".png"))
	} else {
		img = loadFixture(t, n)
	}
	capCache[key] = img
	return img
}

func decodeRGBA(t testing.TB, path string) *image.RGBA {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	dst := image.NewRGBA(src.Bounds())
	draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
	return dst
}

// loadFixture rebuilds a full 1920x1050 frame: JPEG background + lossless ROI.
func loadFixture(t testing.TB, n string) *image.RGBA {
	t.Helper()
	img := decodeRGBA(t, fixtures+n+".jpg")
	roi := decodeRGBA(t, fixtures+n+".roi.png") // RGBA: alpha 0 outside the ROI
	for i := 0; i < len(roi.Pix); i += 4 {
		if roi.Pix[i+3] == 255 {
			copy(img.Pix[i:i+3], roi.Pix[i:i+3])
		}
	}
	return img
}

// detectors: every panel detector, by the panel it names.
var detectors = []struct {
	p  Panel
	fn func(image.Image) bool
}{
	{PauseMenu, PauseMenuVisible},
	{SubPanel, func(i image.Image) bool { _, _, ok := SubPanelX(i); return ok }},
	{NPCMenu, func(i image.Image) bool { return NPCMenuSight(i).Seen }},
	{NPCDialog, func(i image.Image) bool { return NPCDialogSight(i).Seen }},
	{Shop, ShopVisible},
	{Inventory, func(i image.Image) bool { return InventorySight(i).Seen }},
	{CharSheet, func(i image.Image) bool { return CharSheetSight(i).Seen }},
	{SkillTree, func(i image.Image) bool { return SkillTreeSight(i).Seen }},
	{QuestLog, func(i image.Image) bool { return QuestLogSight(i).Seen }},
	{Stash, func(i image.Image) bool { return StashSight(i).Seen }},
	{Waypoint, func(i image.Image) bool { return WaypointSight(i).Seen }},
	{Chat, func(i image.Image) bool { return ChatSight(i).Seen }},
	{SkillPicker, func(i image.Image) bool { return SkillPickerSight(i).Seen }},
	{Mercenary, func(i image.Image) bool { return MercenarySight(i).Seen }},
	{Automap, func(i image.Image) bool { return AutomapSight(i).Seen }},
}

// THE VALIDATION MATRIX: every detector on every capture (8 old + 27 R2 +
// the R9 lair frame). A
// detector must fire exactly on the captures that show its panel — the clear
// towns of both acts, the dark field, both pause menus and the shops included.
func TestValidationMatrix(t *testing.T) { validationMatrix(t, false) }

// TestMatrixOnOriginals: the same matrix on the untouched R2 PNGs.
func TestMatrixOnOriginals(t *testing.T) {
	if os.Getenv("AZBOT_R2_DIR") == "" || os.Getenv("AZBOT_R9_DIR") == "" {
		t.Skip("AZBOT_R2_DIR / AZBOT_R9_DIR not set (relay/R2, relay/R9 originals)")
	}
	validationMatrix(t, true)
}

func validationMatrix(t *testing.T, orig bool) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "\n%-32s", "capture \\ detector")
	for _, d := range detectors {
		fmt.Fprintf(&sb, " %4.4s", d.p.String())
	}
	sb.WriteString("\n")
	for _, name := range allCaptures() {
		img := loadAny(t, name, orig)
		fmt.Fprintf(&sb, "%-32s", name)
		for _, d := range detectors {
			got, want := d.fn(img), truth[name]&d.p != 0
			cell := "   ."
			switch {
			case got && want:
				cell = "   X"
			case got:
				cell = "  FP"
			case want:
				cell = "  FN"
			}
			sb.WriteString(" " + cell)
			if got != want {
				t.Errorf("%s: %s detector = %v, want %v", name, d.p, got, want)
			}
		}
		sb.WriteString("\n")
	}
	t.Log(sb.String())
}

// Observe names exactly the panels on every capture, by sight alone, with the
// close button each panel shows — and never falls back to a generic half panel
// (every framed panel on these captures has a name now).
func TestObserveAllCaptures(t *testing.T) {
	wantX := map[Panel]Point{
		SubPanel: {1413, 81}, PauseMenu: {960, 685}, Shop: {657, 120}, CharSheet: {657, 120},
		QuestLog: {657, 120}, Waypoint: {657, 120}, Mercenary: {657, 120}, Stash: {946, 18},
		Inventory: {1788, 18}, SkillTree: {1785, 120},
	}
	for _, name := range allCaptures() {
		r := Observe(loadAny(t, name, false), world)
		want := truth[name]
		if r.Panels != want || r.Sight != want || r.Unsure != 0 {
			t.Errorf("%s: panels=%s sight=%s unsure=%s, want %s", name, r.Panels, r.Sight, r.Unsure, want)
		}
		for _, q := range All {
			if r.Panels&q == 0 {
				continue
			}
			if r.Evidence[q] == "" {
				t.Errorf("%s: %s has no evidence", name, q)
			}
			if pt, ok := wantX[q]; ok && r.Close[q] != pt {
				t.Errorf("%s: %s close %v, want %v", name, q, r.Close[q], pt)
			}
		}
		t.Logf("%-32s %s — %s", name, r, r.Why(r.Panels))
	}
}

// Two panels at once read as both, each with its own X.
func TestTwoPanelCaptures(t *testing.T) {
	for name, want := range map[string]Panel{
		"r2/inv_and_sheet": Inventory | CharSheet, "r2/waypoint_act1_and_inventory": Waypoint | Inventory,
		"r2/stash": Stash | Inventory, "shop_open": Shop | Inventory,
	} {
		r := Observe(loadAny(t, name, false), world)
		if !r.Sight.Has(want) {
			t.Errorf("%s: sight %s, want %s", name, r.Sight, want)
		}
		for _, q := range All {
			if want&q != 0 {
				if _, ok := r.Close[q]; !ok {
					t.Errorf("%s: no close button for %s", name, q)
				}
			}
		}
	}
}

// OBSERVE MUST STAY CHEAP: it runs several times a second on the executive's
// tick. Measured budget: < 5 ms per full 1920x1050 frame.
func BenchmarkObserve(b *testing.B) {
	for _, name := range []string{"r2/world_town", "r2/inv_and_sheet", "r2/npc_menu_3", "town_after_trade"} {
		var img image.Image
		if strings.HasPrefix(name, "r2/") {
			img = loadFixture(b, strings.TrimPrefix(name, "r2/"))
		} else {
			f, err := os.Open(testdata + name + ".png")
			if err != nil {
				b.Fatal(err)
			}
			src, err := png.Decode(f)
			f.Close()
			if err != nil {
				b.Fatal(err)
			}
			img = src
		}
		b.Run(strings.TrimPrefix(name, "r2/"), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				Observe(img, world)
			}
		})
	}
}

// BenchmarkObserveAll: the mean over all 36 captures, reported per frame.
func BenchmarkObserveAll(b *testing.B) {
	var imgs []image.Image
	for _, name := range allCaptures() {
		if n, ok := cutRelay(name); ok {
			imgs = append(imgs, loadFixture(b, n))
			continue
		}
		imgs = append(imgs, decodeRGBA(b, testdata+name+".png"))
	}
	b.ResetTimer()
	worst := make([]time.Duration, len(imgs))
	for i := 0; i < b.N; i++ {
		for j, img := range imgs {
			t0 := time.Now()
			Observe(img, world)
			if d := time.Since(t0); i == 0 || d < worst[j] {
				worst[j] = d // per capture: its fastest run (noise-free cost)
			}
		}
	}
	b.StopTimer()
	var mx time.Duration
	for _, d := range worst {
		mx = max(mx, d)
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*len(imgs))/1e6, "ms/frame")
	b.ReportMetric(float64(mx.Nanoseconds())/1e6, "worst-ms/frame")
}

// ---------------------------------------------------------------- Fixture writer

// recImg records every pixel a detector reads (it defeats the Pix fast path,
// so every read goes through At).
type recImg struct {
	*image.RGBA
	on   bool
	seen map[image.Point]bool
}

func (r *recImg) At(x, y int) color.Color {
	if r.on {
		r.seen[image.Point{x, y}] = true
	}
	return r.RGBA.At(x, y)
}

func TestWriteFixtures(t *testing.T) {
	if os.Getenv("AZBOT_R2_DIR") == "" || os.Getenv("AZBOT_R9_DIR") == "" || os.Getenv("AZBOT_WRITE_FIXTURES") != "1" {
		t.Skip("set AZBOT_R2_DIR, AZBOT_R9_DIR and AZBOT_WRITE_FIXTURES=1 to regenerate testdata/")
	}
	if err := os.MkdirAll(fixtures, 0o755); err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, name := range relayFixtures() {
		n, _ := cutRelay(name)
		src := decodeRGBA(t, filepath.Join(relayDir(name), n+".png"))
		rec := &recImg{RGBA: src, on: true, seen: map[image.Point]bool{}}
		// Every fixed-position detector, recorded. The NPC-box scan is not: it
		// sweeps the central band, which stays JPEG except for a real box.
		for _, d := range detectors {
			if d.p != NPCMenu && d.p != NPCDialog {
				d.fn(rec)
			}
		}
		LeftPanelScore(rec)
		RightPanelScore(rec)
		xTree.find(rec)
		xStash.find(rec)
		rec.on = false
		for _, b := range NPCBoxes(src) {
			for y := b.Y0 - 6; y <= b.Y1+6; y++ {
				for x := b.X0 - 6; x <= b.X1+6; x++ {
					rec.seen[image.Point{x, y}] = true
				}
			}
		}
		roi := image.NewNRGBA(src.Rect)
		pts := make([]image.Point, 0, len(rec.seen))
		for p := range rec.seen {
			pts = append(pts, p)
		}
		sort.Slice(pts, func(i, j int) bool { return pts[i].Y < pts[j].Y || pts[i].Y == pts[j].Y && pts[i].X < pts[j].X })
		for _, p := range pts {
			if p.In(src.Rect) {
				i, o := roi.PixOffset(p.X, p.Y), src.PixOffset(p.X, p.Y)
				copy(roi.Pix[i:i+3], src.Pix[o:o+3])
				roi.Pix[i+3] = 255
			}
		}
		var jb, pb bytes.Buffer
		if err := jpeg.Encode(&jb, src, &jpeg.Options{Quality: 65}); err != nil {
			t.Fatal(err)
		}
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&pb, roi); err != nil {
			t.Fatal(err)
		}
		for path, b := range map[string][]byte{fixtures + n + ".jpg": jb.Bytes(), fixtures + n + ".roi.png": pb.Bytes()} {
			if err := os.WriteFile(path, b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		total += int64(jb.Len() + pb.Len())
		t.Logf("%-28s roi %6d px  jpg %7d B  roi.png %6d B", n, len(pts), jb.Len(), pb.Len())
	}
	t.Logf("total %.2f MB", float64(total)/1e6)
}

// TestSightScores logs every raw measurement behind the matrix (the numbers
// quoted in the detectors' comments) and pins the margins: the weakest
// positive vs the strongest non-positive over all 36 captures.
func TestSightScores(t *testing.T) {
	type row struct {
		name string
		p    Panel
		v    func(image.Image) float64
		min  float64 // every positive at least
		max  float64 // every non-positive at most
	}
	glyph := func(s glyphSig) func(image.Image) float64 {
		return func(i image.Image) float64 { return float64(s.score(i).On) }
	}
	chrome := func(s chromeSig) func(image.Image) float64 {
		return func(i image.Image) float64 { return float64(s.score(i)) }
	}
	chatMin := func(i image.Image) float64 {
		s := ChatScore(i)
		return min(s[0], s[1], s[2], s[3])
	}
	autoText := func(i image.Image) float64 { n, _ := AutomapScore(i); return float64(n) }
	left := Shop | CharSheet | QuestLog | Waypoint | Mercenary
	rows := []row{
		{"title CHARACTER on/20", CharSheet, glyph(titleCharSheet), 20, 6},
		{"title QUEST LOG on/20", QuestLog, glyph(titleQuestLog), 20, 6},
		{"title WAYPOINT on/20", Waypoint, glyph(titleWaypoint), 20, 8},
		{"title MERCENARY on/20", Mercenary, glyph(titleMercenary), 20, 2},
		{"title STASH on/20", Stash, glyph(titleStash), 20, 0},
		{"title SKILL TREE on/20", SkillTree, glyph(titleSkillTree), 20, 0},
		{"hint skill picker on/20", SkillPicker, glyph(hintSkillPicker), 20, 0},
		{"bag chrome /24", Inventory, chrome(chromeBag), 24, 3},
		{"vendor chrome /16", Shop, chrome(chromeVendor), 16, 1},
		{"chat weakest rule", Chat, chatMin, 1, 0.25},
		{"automap name px", Automap, autoText, 117, 0}, // "PIT LEVEL 1" is a short name
		{"bag X red", Inventory, xBag.redFrac, 0.78, 0},
		{"stash X red", Stash, xStash.redFrac, 0.78, 0.16}, // the paused Maggot Lair: red scenery under the dim
		{"tree X red", SkillTree, xTree.redFrac, 0.78, 0},
		{"left X red", left, xLeft.redFrac, 0.78, 0},
		{"sub X red", SubPanel, xSub.redFrac, 0.78, 0},
		{"sub frame /16", SubPanel, chrome(chromeSub), 16, 0},
	}
	for _, r := range rows {
		lo, hi := 1e9, -1e9
		loName, hiName := "", ""
		for _, name := range allCaptures() {
			v := r.v(loadAny(t, name, false))
			if truth[name]&r.p != 0 {
				if v < lo {
					lo, loName = v, name
				}
			} else if v > hi {
				hi, hiName = v, name
			}
		}
		t.Logf("%-24s positives >= %.2f (%s)   others <= %.2f (%s)", r.name, lo, loName, hi, hiName)
		if lo < r.min-0.005 || hi > r.max+0.005 {
			t.Errorf("%s: margin moved: positives >= %.2f (want %.2f), others <= %.2f (want %.2f)", r.name, lo, r.min, hi, r.max)
		}
	}
	for _, name := range allCaptures() {
		for _, b := range NPCBoxes(loadAny(t, name, false)) {
			t.Logf("%-32s %s", name, b)
		}
	}
}
