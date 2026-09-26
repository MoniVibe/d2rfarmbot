package screen

import (
	"fmt"
	"image"
)

// ---------------------------------------------------------------- Photographed panels
//
// Relay R2 (2026-09-24, laptop, 1920x1050 client): every keyboard panel was
// captured beside clear towns (Act 1 and Act 2), a dark outdoor field and the
// pause menu, and memory read MenuOpen(0xF4)=false and QuitMenu=false for
// every keyboard panel and the pause menu — the screen is the only truth.
// Layout as measured:
//
//	left  (x 118-678, y 100-830, X at 657,120): vendor, char sheet (C), quest
//	      log (Q), waypoint, mercenary (O)
//	left, wide (x 118-962, y 0-870, X at 946,18): stash — always with the bag
//	right (x 1245-1805, y 0-870, X at 1788,18): inventory (I)
//	right (x 1245-1805, y 100-830, X at 1785,120): skill tree (T)
//	floating: NPC menu (gold-bordered black box above the NPC), NPC speech
//	      (the same frame, 388 wide, top center)
//	bottom-left: chat (Enter); bottom center: skill picker; top-right: the
//	      automap's area name (Tab)
//
// Titled panels are named by their title glyphs (glyphSig); the untitled bag
// and vendor by their static chrome (chromeSig). Measured "a/b vs c/b" below
// is positives vs the best non-positive over all 34 captures.

// Panel titles (gold, 20 on + 12 off points each; seen at >= 15 on + 9 off).
// Measured 20/20 + 12/12 on every capture showing the panel. At the exact
// place every other capture reads 0/20 — the other left titles (CHARACTER /
// QUEST LOG / WAYPOINT / the vendor's name) share band and color, so these
// points are exactly where the others are NOT gold; with the 1-px shift search
// the best other word reaches 6/20 (CHARACTER, QUEST LOG), 8/20 (WAYPOINT vs
// the char sheet), 2/20 (MERCENARY), 0/20 (STASH, SKILL TREE).
var (
	titleCharSheet = glyphSig{name: "CHARACTER", a: leftEdge, text: titleGold, minOn: 15, minOff: 9,
		on: [][2]int{{333, 140}, {414, 144}, {355, 145}, {367, 145}, {433, 145}, {424, 146}, {397, 147}, {454, 147}, {347, 148}, {381, 151},
			{327, 152}, {428, 152}, {371, 153}, {403, 155}, {461, 155}, {335, 156}, {361, 156}, {388, 156}, {417, 156}, {442, 156}},
		off: [][2]int{{319, 160}, {320, 139}, {324, 157}, {340, 141}, {351, 158}, {368, 139}, {368, 157}, {378, 160}, {393, 146}, {406, 150},
			{407, 158}, {415, 139}}}
	titleQuestLog = glyphSig{name: "QUEST LOG", a: leftEdge, text: titleGold, minOn: 15, minOff: 9,
		on: [][2]int{{336, 140}, {420, 141}, {344, 144}, {385, 144}, {457, 144}, {374, 145}, {400, 145}, {437, 145}, {361, 147}, {451, 148},
			{330, 150}, {421, 150}, {351, 153}, {387, 154}, {443, 154}, {460, 155}, {399, 156}, {425, 156}, {368, 157}, {337, 159}},
		off: [][2]int{{319, 160}, {322, 139}, {334, 139}, {345, 157}, {365, 148}, {380, 156}, {388, 139}, {399, 160}, {401, 154}, {424, 139},
			{427, 139}, {432, 160}}}
	titleWaypoint = glyphSig{name: "WAYPOINT", a: leftEdge, text: titleGold, minOn: 15, minOff: 9,
		on: [][2]int{{346, 141}, {354, 143}, {333, 144}, {384, 146}, {405, 146}, {448, 146}, {458, 146}, {342, 147}, {376, 147}, {423, 147},
			{432, 147}, {414, 148}, {366, 150}, {349, 151}, {396, 152}, {437, 152}, {339, 154}, {411, 156}, {431, 156}, {453, 156}},
		off: [][2]int{{319, 139}, {322, 139}, {329, 153}, {344, 156}, {346, 160}, {360, 139}, {367, 157}, {384, 150}, {404, 159}, {413, 139},
			{415, 156}, {433, 160}}}
	// The hireling panel carries its title ON the frame's top edge (y 107-123).
	titleMercenary = glyphSig{name: "MERCENARY", a: leftEdge, text: titleGold, minOn: 15, minOff: 9,
		on: [][2]int{{326, 107}, {337, 107}, {384, 110}, {352, 111}, {366, 111}, {397, 111}, {418, 111}, {448, 111}, {466, 111}, {342, 114},
			{432, 114}, {334, 117}, {379, 118}, {328, 122}, {358, 122}, {410, 122}, {425, 122}, {442, 122}, {462, 122}, {395, 123}},
		off: [][2]int{{319, 126}, {344, 115}, {360, 120}, {362, 126}, {374, 109}, {401, 106}, {410, 126}, {416, 110}, {419, 126}, {421, 111},
			{436, 126}, {437, 125}}}
	titleStash = glyphSig{name: "STASH", a: leftEdge, text: titleGold, minOn: 15, minOff: 9,
		on: [][2]int{{510, 40}, {556, 44}, {506, 45}, {520, 45}, {566, 45}, {531, 46}, {575, 46}, {525, 48}, {542, 48}, {553, 48},
			{512, 49}, {570, 49}, {558, 51}, {506, 54}, {536, 54}, {546, 54}, {567, 54}, {555, 55}, {525, 56}, {575, 56}},
		off: [][2]int{{497, 39}, {527, 39}, {550, 39}, {576, 39}, {538, 45}, {512, 46}, {530, 51}, {563, 52}, {544, 56}, {501, 57},
			{523, 58}, {584, 58}}}
	titleSkillTree = glyphSig{name: "SKILL TREE", a: rightEdge, text: titleGold, minOn: 15, minOff: 9,
		on: [][2]int{{1460, 140}, {1532, 141}, {1546, 142}, {1489, 145}, {1510, 145}, {1575, 145}, {1587, 145}, {1538, 146}, {1498, 147}, {1463, 149},
			{1560, 149}, {1476, 151}, {1581, 151}, {1456, 154}, {1553, 155}, {1517, 156}, {1540, 156}, {1569, 156}, {1589, 156}, {1497, 157}},
		off: [][2]int{{1447, 160}, {1463, 139}, {1479, 151}, {1483, 151}, {1493, 139}, {1493, 151}, {1505, 160}, {1508, 151}, {1516, 139}, {1532, 151},
			{1536, 151}, {1544, 139}}}
	// "PRESS F1-F8 TO BIND A SKILL" — white on a dark band just above the bottom
	// bar, drawn while the skill picker is up (both captures 20/20 + 12/12).
	hintSkillPicker = glyphSig{name: "PRESS F1-F8 TO BIND A SKILL", a: center, text: hintWhite, minOn: 15, minOff: 9,
		on: [][2]int{{806, 917}, {819, 920}, {844, 920}, {946, 920}, {964, 920}, {998, 920}, {1022, 920}, {1067, 920}, {1099, 920}, {832, 921},
			{1047, 921}, {1111, 921}, {986, 923}, {1013, 924}, {1081, 926}, {858, 928}, {826, 929}, {951, 929}, {1005, 929}, {1116, 929}},
		off: [][2]int{{801, 913}, {852, 913}, {918, 913}, {970, 913}, {1054, 913}, {1096, 913}, {826, 932}, {885, 932}, {944, 932}, {1012, 932},
			{1075, 932}, {1120, 932}}}
)

// chromeBag: the inventory's untitled stone — frame top, the weapon-swap tabs
// and the stone between the equipment slots (slots, grid and gold excluded).
// Measured 24/24 on all nine bag captures (alone, beside the vendor, the stash,
// the char sheet and the Act 1 waypoint), at most 3/24 elsewhere (waypoint.png;
// the skill tree shares the frame but starts at y=100). Seen at >= 18.
var chromeBag = chromeSig{name: "bag", a: rightEdge, tol: 30, min: 18, pts: [][5]int{
	{1312, 12, 60, 60, 60}, {1366, 12, 71, 71, 69}, {1408, 12, 73, 73, 72}, {1438, 12, 77, 77, 75}, {1522, 12, 75, 75, 74},
	{1690, 12, 87, 87, 82}, {1468, 42, 74, 74, 71}, {1540, 42, 67, 68, 65}, {1570, 42, 74, 75, 72}, {1606, 42, 70, 71, 68},
	{1426, 48, 81, 81, 79}, {1792, 48, 69, 68, 67}, {1306, 60, 71, 77, 70}, {1630, 60, 56, 57, 56}, {1690, 60, 65, 70, 63},
	{1666, 66, 67, 72, 65}, {1444, 72, 87, 87, 83}, {1588, 72, 68, 68, 66}, {1618, 78, 64, 64, 62}, {1600, 114, 55, 56, 54},
	{1438, 126, 87, 87, 85}, {1582, 156, 54, 54, 53}, {1792, 156, 56, 55, 56}, {1450, 168, 68, 68, 68},
}}

// chromeVendor: the trade panel's gold box and bottom stone (the title is the
// NPC's name and the grid is stock, so neither is static). Measured 16/16 on
// both shop captures, at most 1/16 on anything else — the char sheet, quest
// log, waypoint and mercenary panels that share its frame included. Seen at
// >= 12.
var chromeVendor = chromeSig{name: "vendor", a: leftEdge, tol: 14, min: 12, pts: [][5]int{
	{328, 724, 79, 78, 75}, {370, 724, 101, 99, 92}, {484, 748, 65, 65, 62}, {154, 760, 34, 34, 36}, {328, 772, 48, 49, 49},
	{346, 772, 46, 47, 47}, {364, 772, 44, 44, 45}, {382, 772, 43, 43, 43}, {406, 772, 41, 41, 42}, {448, 772, 58, 58, 57},
	{472, 772, 51, 50, 47}, {292, 784, 32, 32, 33}, {202, 796, 37, 38, 39}, {226, 796, 39, 39, 40}, {262, 796, 33, 33, 35},
	{598, 796, 42, 43, 44},
}}

// Sighting is one detector's verdict on a capture.
type Sighting struct {
	Seen  bool
	Why   string // measurement, for Evidence
	Close Point  // the panel's X, when seen on the capture
	HasX  bool
}

// titled: a panel named by its title plus the X its frame carries.
func titled(img image.Image, sig glyphSig, x closeSpot) Sighting {
	sc := sig.score(img)
	s := Sighting{Seen: sig.match(sc), Why: fmt.Sprintf("title %q %d/%d+%d/%d", sig.name, sc.On, sc.NOn, sc.Off, sc.NOff)}
	if cx, cy, ok := x.find(img); ok && s.Seen {
		s.Close, s.HasX = Point{cx, cy}, true
		s.Why += " + red X"
	}
	return s
}

// CharSheetSight: the char sheet (C), left, by its CHARACTER title.
func CharSheetSight(img image.Image) Sighting { return titled(img, titleCharSheet, xLeft) }

// QuestLogSight: the quest log (Q), left.
func QuestLogSight(img image.Image) Sighting { return titled(img, titleQuestLog, xLeft) }

// WaypointSight: the waypoint list, left — the only proof: OpenMenus.Waypoint
// LINGERS true after the panel closes (usewaypoint.go, 03:08/03:18).
func WaypointSight(img image.Image) Sighting { return titled(img, titleWaypoint, xLeft) }

// MercenarySight: the hireling panel (O), left, title on the frame's top edge.
func MercenarySight(img image.Image) Sighting { return titled(img, titleMercenary, xLeft) }

// StashSight: the stash, the wide left panel (x 118-962); the bag comes with it.
func StashSight(img image.Image) Sighting { return titled(img, titleStash, xStash) }

// SkillTreeSight: the skill tree (T) — RIGHT side, y 100-830, its own X at
// (1785,120), 102 px below the bag's.
func SkillTreeSight(img image.Image) Sighting { return titled(img, titleSkillTree, xTree) }

// InventorySight: the bag (I), right, by its chrome — alone or beside the
// vendor / stash / a left panel. Its X is (1788,18).
func InventorySight(img image.Image) Sighting {
	n := chromeBag.score(img)
	s := Sighting{Seen: n >= chromeBag.min, Why: fmt.Sprintf("bag chrome %d/%d", n, len(chromeBag.pts))}
	if x, y, ok := xBag.find(img); ok && s.Seen {
		s.Close, s.HasX = Point{x, y}, true
		s.Why += " + red X"
	}
	return s
}

// SkillPickerSight: the skill picker popup, by its fixed hint line. No X: the
// picker closes on ESC (or on choosing a skill, which the janitor must not do).
func SkillPickerSight(img image.Image) Sighting {
	sc := hintSkillPicker.score(img)
	return Sighting{Seen: hintSkillPicker.match(sc),
		Why: fmt.Sprintf("hint %q %d/%d+%d/%d", hintSkillPicker.name, sc.On, sc.NOn, sc.Off, sc.NOff)}
}

// ---------------------------------------------------------------- Chat

// THE OPEN CHAT LINE IS FOUR GREY RULES. With Enter pressed the chat pane
// (x 19-600) draws 1-px (67,67,67) rules at y 406 and 436 (the "Game" header)
// and 816 and 858 (the input line with the channel icon) over a translucent
// dark pane. Measured 2026-09-24 as the fraction of 29 samples along each rule
// (x 80-584, any row within +-1): chat.png 1.00 on all four; every other
// capture 0.00 on its weakest rule. The closed chat (messages fading over the
// world) draws no rules.
var chatRules = []int{406, 436, 816, 858}

const chatRuleMin = 0.80

func chatGrey(r, g, b int) bool {
	return max(r, g, b)-min(r, g, b) <= 8 && r >= 55 && r <= 80
}

// ChatScore: per rule, the fraction of samples on the grey line.
func ChatScore(img image.Image) [4]float64 {
	var s [4]float64
	if !usable(img) {
		return s
	}
	for i, ry := range chatRules {
		n, hit := 0, 0
		for rx := 80; rx <= 584; rx += 18 {
			n++
			for dy := -1; dy <= 1; dy++ {
				x, y := fromLeft(img, rx, ry)
				if chatGrey(pix(img, x, y+dy)) {
					hit++
					break
				}
			}
		}
		s[i] = float64(hit) / float64(n)
	}
	return s
}

// ChatSight: the chat input line is open (it swallows every skill key).
func ChatSight(img image.Image) Sighting {
	s := ChatScore(img)
	seen := true
	for _, f := range s {
		if f < chatRuleMin {
			seen = false
		}
	}
	return Sighting{Seen: seen, Why: fmt.Sprintf("chat rules %.2f/%.2f/%.2f/%.2f", s[0], s[1], s[2], s[3])}
}

// ---------------------------------------------------------------- Automap

// THE AUTOMAP NAMES THE AREA. With Tab's overlay up, the area name is drawn
// right-aligned at the top-right corner (text 199,179,119, ending x~1893,
// y 40-51) over the corner's permanent dark vignette. Measured 2026-09-24 in
// x 1812-1898, y 36-55: 144 beige pixels on automap_overlay and on BOTH old
// shop captures (their automap was up — the "…T GHOLEIN" beside the bag),
// 0 on all 31 others; the band reads >=89% dark on every capture.
const (
	automapTextMin  = 30
	automapTextMax  = 600 // a lit wall, not text
	automapDarkFrac = 0.50
)

// AutomapScore: beige text pixels and the dark fraction in the name band.
func AutomapScore(img image.Image) (text int, dark float64) {
	if !usable(img) {
		return 0, 0
	}
	n, d := 0, 0
	for ry := 36; ry < 56; ry++ {
		for rx := 1812; rx < 1900; rx++ {
			x, y := fromRight(img, rx, ry)
			R, G, B := pix(img, x, y)
			n++
			if max(R, G, B) <= 50 {
				d++
			}
			if R >= 130 && R <= 235 && G*100 >= R*80 && G*100 <= R*97 && B*100 >= R*45 && B*100 <= R*75 {
				text++
			}
		}
	}
	return text, float64(d) / float64(n)
}

// AutomapSight: the automap overlay is up (non-blocking; Tab toggles it).
func AutomapSight(img image.Image) Sighting {
	t, d := AutomapScore(img)
	return Sighting{Seen: t >= automapTextMin && t <= automapTextMax && d >= automapDarkFrac,
		Why: fmt.Sprintf("area name %d px, corner %.2f dark", t, d)}
}
