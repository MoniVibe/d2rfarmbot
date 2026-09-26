// Package learn is the combat ESTIMATOR: what each strike skill actually
// buys in each kind of situation, measured strike by strike and shrunk
// toward priors while the counts are small. Pure — no game, no input, no
// clock of its own. The Fight activity feeds it telemetry (track.go) and
// combat/policy reads its estimates to choose between Leap Attack and the
// owner's Carnage.
//
// The owner, 2026-09-24: "Carnage is fine, but be more dynamic on how it
// approaches mobs. Leap Attack does AoE so it clears swarms more easily;
// Carnage is more single target. We can math this out if we gather enough
// telemetry." This package is the math.
package learn

import (
	"fmt"
	"math"
	"sort"
	"strings"
)

// Skill names the estimator keys on (stable across mod table renames).
const (
	Leap    = "leap"    // Leap Attack (143), right, cursor-targeted to a ground point
	Carnage = "carnage" // the owner's left skill (144, "Concentrate" in the mod table)
	Swing   = "ds"      // Double Swing (133), the fallback
	Basic   = "basic"   // plain attack / an owner-declared generic binding
)

// Pt is a world tile position.
type Pt struct{ X, Y int }

// Cheb is the Chebyshev (king-move) distance in tiles.
func Cheb(a, b Pt) int {
	dx, dy := a.X-b.X, a.Y-b.Y
	if dx < 0 {
		dx = -dx
	}
	if dy < 0 {
		dy = -dy
	}
	if dx > dy {
		return dx
	}
	return dy
}

// Dist is the Euclidean distance in tiles — the AoE metric (a landing
// splash is round, not square).
func Dist(a, b Pt) float64 {
	return math.Hypot(float64(a.X-b.X), float64(a.Y-b.Y))
}

// Within reports whether p lies within radius r (tiles, Euclidean, half a
// tile of slack for the grid) of c.
func Within(c, p Pt, r int) bool {
	dx, dy := float64(p.X-c.X), float64(p.Y-c.Y)
	rr := float64(r) + 0.5
	return dx*dx+dy*dy <= rr*rr
}

// ---------------------------------------------------------------- buckets

// BucketRadius: the cluster of a strike is the number of live enemies within
// this many tiles of its IMPACT point (leap: the aim point; carnage: the
// target), the struck one included. Fixed — the learned AoE radius moves the
// aim, never the bucket keys, so stored statistics stay comparable.
const BucketRadius = 3

// NCluster / NDist are the bucket counts.
const (
	NCluster = 4
	NDist    = 4
)

// ClusterNames / DistNames label the buckets in logs and tables.
var (
	ClusterNames = [NCluster]string{"1", "2-3", "4-6", "7+"}
	DistNames    = [NDist]string{"melee", "4-5", "6-9", "10+"}
)

// ClusterBucket maps a cluster count onto {1, 2-3, 4-6, 7+} (0 reads as 1:
// a strike always has something at its impact point by intent).
func ClusterBucket(n int) int {
	switch {
	case n <= 1:
		return 0
	case n <= 3:
		return 1
	case n <= 6:
		return 2
	}
	return 3
}

// DistBucket maps a Chebyshev distance onto {melee ≤3, 4-5, 6-9, 10+}.
func DistBucket(d int) int {
	switch {
	case d <= 3:
		return 0
	case d <= 5:
		return 1
	case d <= 9:
		return 2
	}
	return 3
}

// Key is one estimator cell.
type Key struct {
	Skill string
	C, D  int // bucket indices
}

func (k Key) String() string {
	return fmt.Sprintf("%s/%s/%s", k.Skill, ClusterNames[k.C], DistNames[k.D])
}

// BucketLabel is the "cluster/dist" label of a cell (the decision log's bucket=).
func BucketLabel(c, d int) string { return ClusterNames[c] + "/" + DistNames[d] }

// ---------------------------------------------------------------- priors

// Prior is what a cell believes before it has data.
type Prior struct {
	ActionSec     float64 // how long one action occupies the hands (incl. walk-in)
	KillsPerSec   float64
	HitsPerAction float64
	HPLossPerSec  float64 // HP percent points lost per second of action
	ManaPerAction float64 // mana percent points spent per action
}

// WalkTilesPerSec is the running pace a walk-in is costed at (a barbarian
// runs ~6 tiles/s; 5 leaves room for the path not being straight).
const WalkTilesPerSec = 5.0

// walkIn is the walk time a melee skill adds before it can swing from the
// middle of distance bucket d (Carnage needs melee range: the gap is walked).
func walkIn(d int) float64 {
	mid := [NDist]float64{0, 4.5, 7.5, 12}[d]
	if gap := mid - 3; gap > 0 {
		return gap / WalkTilesPerSec
	}
	return 0
}

// PriorFor is the hand-set prior of a cell (see the package doc):
//
//   - leap: AoE with prior radius 3 — kills scale with the cluster at the
//     landing (strong at 4+), one 0.9s action (jump + landing), about a
//     quarter of the pool per leap (relay R9: MP 248→220→194 over two
//     leaps), a little more HP exposure (it lands inside the pack); weaker
//     at 10+ (over-range asks were refused or deaf in R9).
//   - carnage: single target, strong at melee cluster 1 (R9: 48 of 84 left
//     strikes credited, most one-swing kills); a flat kill rate over the
//     cluster (a denser pack only means the next body is already in reach);
//     beyond melee the walk-in time is ADDED to the action time.
//   - ds / basic: the same shape as carnage, weaker.
//
// R9 SEEDS (cmd/strikereplay over relay R9's 155 placeable strikes): leap
// at a lone body measured 0.35-0.38 kills/s at 4-9 tiles (31 strikes — the
// 0.40 prior stands); leap into 2-3 bodies 1.48 (13) and into 4-6 1.92 (7)
// at 4-5 tiles — the cluster column was raised to 1.0 / 1.7 / 2.5; Carnage
// on a lone melee body 0.69 (19) against 1.21 on 2-3 (21) — its lone-body
// prior dropped 1.20 → 0.90 (still well above the leap's 0.34 there). R9's
// mana column is not usable (1.4s flight frames miss the per-strike dip;
// R9's status lines show ~27 points per leap), so mana stays hand-set.
func PriorFor(skill string, c, d int) Prior {
	switch skill {
	case Leap:
		kps := [NCluster]float64{0.40, 1.00, 1.70, 2.50}[c]
		kps *= [NDist]float64{0.85, 1.0, 0.9, 0.6}[d]
		return Prior{ActionSec: 0.9, KillsPerSec: kps,
			HitsPerAction: [NCluster]float64{0.5, 1.2, 2.5, 4.0}[c],
			HPLossPerSec:  [NCluster]float64{0.3, 0.5, 0.9, 1.4}[c],
			ManaPerAction: 25}
	case Carnage, Swing, Basic:
		base := map[string][NCluster]float64{
			Carnage: {0.90, 1.25, 1.30, 1.35},
			Swing:   {0.85, 0.90, 0.95, 1.00},
			Basic:   {0.50, 0.55, 0.60, 0.65},
		}[skill][c]
		mana := map[string]float64{Carnage: 2, Swing: 5, Basic: 0}[skill]
		const a = 0.45
		w := walkIn(d)
		return Prior{ActionSec: a + w, KillsPerSec: base * a / (a + w),
			HitsPerAction: 0.75,
			HPLossPerSec:  [NCluster]float64{0.2, 0.3, 0.5, 0.8}[c],
			ManaPerAction: mana}
	}
	return Prior{ActionSec: 0.5}
}

// ---------------------------------------------------------------- the model

// Stats are one cell's raw running sums.
type Stats struct {
	N         int     `json:"n"`
	ActionSec float64 `json:"t"`
	Kills     float64 `json:"k"`
	Hits      float64 `json:"h"`
	HPLoss    float64 `json:"hp"`
	Mana      float64 `json:"mp"`
}

// Sample is one closed strike's measurement.
type Sample struct {
	Skill     string
	Cluster   int // raw count within BucketRadius of the impact point
	Dist      int // raw Chebyshev distance me→impact point
	ActionSec float64
	Kills     float64
	Hits      float64
	HPLoss    float64
	Mana      float64
}

// Estimate is a cell's shrunk belief.
type Estimate struct {
	KillsPerSec, HitsPerAction, HPLossPerSec, ManaPerAction, ActionSec float64
	N                                                                  int
}

// PriorStrength is the shrinkage weight: the prior counts as this many
// actions of evidence. A cell's estimate is half data, half prior at 6
// strikes and 90% data at 54.
const PriorStrength = 6.0

// Model is the estimator: the cells plus the leap AoE-radius histogram.
type Model struct {
	Cells map[string]*Stats `json:"cells"`
	AoE   AoEHist           `json:"aoe"`
}

// NewModel is an empty model (pure priors).
func NewModel() *Model { return &Model{Cells: map[string]*Stats{}} }

func (m *Model) cell(k Key) *Stats {
	if m.Cells == nil {
		m.Cells = map[string]*Stats{}
	}
	s := m.Cells[k.String()]
	if s == nil {
		s = &Stats{}
		m.Cells[k.String()] = s
	}
	return s
}

// Observe adds one sample to its cell.
func (m *Model) Observe(s Sample) {
	if s.ActionSec <= 0 {
		return
	}
	st := m.cell(Key{s.Skill, ClusterBucket(s.Cluster), DistBucket(s.Dist)})
	st.N++
	st.ActionSec += s.ActionSec
	st.Kills += s.Kills
	st.Hits += s.Hits
	st.HPLoss += s.HPLoss
	st.Mana += s.Mana
}

// Count is a cell's sample count (nil model: 0).
func (m *Model) Count(skill string, c, d int) int {
	if m == nil || m.Cells == nil {
		return 0
	}
	if s := m.Cells[Key{skill, c, d}.String()]; s != nil {
		return s.N
	}
	return 0
}

// Shrink is the estimator's one formula: a rate observed as sum/exposure,
// pulled toward prior with a pseudo-exposure of k·unit —
// (sum + k·unit·prior) / (exposure + k·unit).
func Shrink(sum, exposure, prior, k, unit float64) float64 {
	return (sum + k*unit*prior) / (exposure + k*unit)
}

// Estimate is the shrunk belief of cell (skill, c, d). Per-second rates use
// the prior's action time as the pseudo-exposure unit; per-action rates use
// one action. A nil model answers pure priors.
func (m *Model) Estimate(skill string, c, d int) Estimate {
	p := PriorFor(skill, c, d)
	var st Stats
	if m != nil && m.Cells != nil {
		if s := m.Cells[Key{skill, c, d}.String()]; s != nil {
			st = *s
		}
	}
	k := PriorStrength
	n := float64(st.N)
	return Estimate{
		KillsPerSec:   Shrink(st.Kills, st.ActionSec, p.KillsPerSec, k, p.ActionSec),
		HPLossPerSec:  Shrink(st.HPLoss, st.ActionSec, p.HPLossPerSec, k, p.ActionSec),
		HitsPerAction: Shrink(st.Hits, n, p.HitsPerAction, k, 1),
		ManaPerAction: Shrink(st.Mana, n, p.ManaPerAction, k, 1),
		ActionSec:     Shrink(st.ActionSec, n, p.ActionSec, k, 1),
		N:             st.N,
	}
}

// AoERadius is the leap splash radius the aim uses: measured from the
// histogram once it has evidence, the prior (3) until then.
func (m *Model) AoERadius() int {
	if m == nil {
		return AoEPrior
	}
	r, _ := m.AoE.Radius()
	return r
}

// Table renders every cell that has data (plus its estimate) — the offline
// check's and the engagement log's view of the model.
func (m *Model) Table() string {
	var keys []string
	for k, s := range m.Cells {
		if s.N > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, "%-22s %4s %7s %7s %7s %7s %6s | %7s %7s\n",
		"cell", "n", "k/s", "hit/a", "hp/s", "mp/a", "act", "est k/s", "prior")
	for _, k := range keys {
		s := m.Cells[k]
		parts := strings.SplitN(k, "/", 3)
		c, d := indexOf(ClusterNames[:], parts[1]), indexOf(DistNames[:], parts[2])
		e := m.Estimate(parts[0], c, d)
		fmt.Fprintf(&b, "%-22s %4d %7.2f %7.2f %7.2f %7.1f %6.2f | %7.2f %7.2f\n",
			k, s.N, s.Kills/s.ActionSec, s.Hits/float64(s.N), s.HPLoss/s.ActionSec, s.Mana/float64(s.N),
			s.ActionSec/float64(s.N), e.KillsPerSec, PriorFor(parts[0], c, d).KillsPerSec)
	}
	return b.String()
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return 0
}

// ---------------------------------------------------------------- AoE radius

// AoEMaxD is the farthest distance (tiles) the leap histogram tracks.
const AoEMaxD = 6

// AoEPrior is the prior leap splash radius (tiles).
const AoEPrior = 3

// AoEHist measures the mod's Leap Attack splash empirically: for every leap,
// each monster within AoEMaxD of the impact point is EXPOSED at its rounded
// distance, and HIT when evidence (GetHit, knockback, death) was credited to
// that leap. hit/exposed by distance is the splash profile.
type AoEHist struct {
	Exposed [AoEMaxD + 1]int `json:"x"`
	Hit     [AoEMaxD + 1]int `json:"h"`
}

// Add records one leap's exposure and evidence distances.
func (h *AoEHist) Add(exposed, hit []float64) {
	for _, d := range exposed {
		if i := int(math.Round(d)); i >= 0 && i <= AoEMaxD {
			h.Exposed[i]++
		}
	}
	for _, d := range hit {
		if i := int(math.Round(d)); i >= 0 && i <= AoEMaxD {
			h.Hit[i]++
		}
	}
}

// aoeShrink / aoeFloor: each distance's hit rate is shrunk toward the prior
// profile (0.6 inside the prior radius, 0.1 outside) with aoeShrink pseudo-
// exposures; the radius is the farthest distance whose rate — and every
// nearer one's — clears aoeFloor.
const (
	aoeShrink = 5.0
	aoeFloor  = 0.35
)

// Rate is the shrunk hit rate at distance d.
func (h AoEHist) Rate(d int) float64 {
	p := 0.1
	if d <= AoEPrior {
		p = 0.6
	}
	return Shrink(float64(h.Hit[d]), float64(h.Exposed[d]), p, aoeShrink, 1)
}

// Radius is the measured splash radius, and whether any distance has
// enough exposure (≥ 2·aoeShrink) to call it measured rather than prior.
func (h AoEHist) Radius() (int, bool) {
	r := 0
	for d := 0; d <= AoEMaxD; d++ {
		if h.Rate(d) < aoeFloor {
			break
		}
		r = d
	}
	if r < 1 {
		r = 1
	}
	measured := false
	for d := 0; d <= AoEMaxD; d++ {
		if h.Exposed[d] >= 2*aoeShrink {
			measured = true
		}
	}
	return r, measured
}

// String is the histogram's log form: "d0=h/x d1=h/x ...".
func (h AoEHist) String() string {
	var b strings.Builder
	for d := 0; d <= AoEMaxD; d++ {
		if d > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "d%d=%d/%d", d, h.Hit[d], h.Exposed[d])
	}
	return b.String()
}
