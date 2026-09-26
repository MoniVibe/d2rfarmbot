package mapfuse

import "fmt"

// Trust thresholds. The prior earns trust per (seed, area) by agreeing with what the
// bot actually OBSERVED (live + atlas), walkable-vs-blocked, over enough cells.
const (
	TrustAgree   = 0.92 // overall agreement rate
	TrustMinN    = 400  // compared cells before any verdict
	TrustMaxDisp = 2    // alignment search: shifts in [-2,2]² cells
	// Open ground dominates observed cells, so an all-walkable prior could reach
	// 0.92 while being blind to the mod's walls. With enough observed walls to
	// judge, the prior must also get most of THEM right.
	TrustWallAgree = 0.80
	TrustWallMinN  = 50
	// Measuring is O(sample × 25 shifts); a deterministic stride keeps it bounded.
	trustSampleCap = 40000
)

// Shift is how far the prior is displaced: prior cell (px,py) describes world
// (px+X, py+Y).
type Shift struct{ X, Y int }

// Trust is one measurement of the prior against observation.
type Trust struct {
	Agree     float64 // agreement rate at the best shift
	N         int     // compared cells (sampled)
	WallAgree float64 // agreement over observed walls
	WallN     int
	Shift     Shift
	Trusted   bool
	Known     int // observed cells when measured (drives re-measurement)
}

func (t Trust) String() string {
	return fmt.Sprintf("agree=%.2f n=%d walls=%.2f/%d shift=(%d,%d) trust=%t",
		t.Agree, t.N, t.WallAgree, t.WallN, t.Shift.X, t.Shift.Y, t.Trusted)
}

// Measure compares observation (live over atlas, restricted to live's frame) with the
// prior at every shift in [-TrustMaxDisp, TrustMaxDisp]², picks the best agreement
// (ties: the smaller displacement, (0,0) first) and judges trust.
func Measure(live, atlas, prior *Layer) Trust {
	type obs struct {
		x, y int
		c    Cell
	}
	var all []obs
	if live != nil {
		for ly := 0; ly < live.H; ly++ {
			for lx := 0; lx < live.W; lx++ {
				wx, wy := lx+live.OffX, ly+live.OffY
				c := live.Cells[ly*live.W+lx]
				if c == Unknown {
					c = atlas.At(wx, wy)
				}
				if c != Unknown {
					all = append(all, obs{wx, wy, c})
				}
			}
		}
	}
	tr := Trust{Known: len(all)}
	if prior == nil || len(all) == 0 {
		return tr
	}
	stride := 1
	if len(all) > trustSampleCap {
		stride = (len(all) + trustSampleCap - 1) / trustSampleCap
	}
	bestRate := -1.0
	for _, s := range shiftOrder() {
		n, agree, wn, wagree := 0, 0, 0, 0
		for k := 0; k < len(all); k += stride {
			o := all[k]
			p := prior.At(o.x-s.X, o.y-s.Y)
			if p == Unknown {
				continue
			}
			n++
			if p == o.c {
				agree++
			}
			if o.c == Block {
				wn++
				if p == Block {
					wagree++
				}
			}
		}
		if n == 0 {
			continue
		}
		rate := float64(agree) / float64(n)
		// Strictly better only: shiftOrder visits small displacements first, so a
		// tie keeps the nearer alignment ((0,0) when nothing beats it).
		if rate > bestRate+1e-9 {
			bestRate = rate
			tr.Agree, tr.N, tr.Shift, tr.WallN = rate, n, s, wn
			tr.WallAgree = 0
			if wn > 0 {
				tr.WallAgree = float64(wagree) / float64(wn)
			}
		}
	}
	tr.Trusted = tr.N >= TrustMinN && tr.Agree >= TrustAgree &&
		(tr.WallN < TrustWallMinN || tr.WallAgree >= TrustWallAgree)
	return tr
}

// shiftOrder lists every shift in the search window, nearest first.
func shiftOrder() []Shift {
	var out []Shift
	for d := 0; d <= 2*TrustMaxDisp; d++ {
		for dy := -TrustMaxDisp; dy <= TrustMaxDisp; dy++ {
			for dx := -TrustMaxDisp; dx <= TrustMaxDisp; dx++ {
				if absInt(dx)+absInt(dy) == d {
					out = append(out, Shift{dx, dy})
				}
			}
		}
	}
	return out
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Key names one (seed, area) — trust is earned per level per world.
type Key struct {
	Seed uint
	Area int
}

// Tracker keeps the latest trust per (seed, area) and re-measures as observation
// grows (rooms stream in): on first sight, then whenever the observed-cell count
// has grown by ≥ remeasureCells or ≥ remeasureFrac since the last measurement.
type Tracker struct {
	m map[Key]Trust
}

const (
	remeasureCells = 400
	remeasureFrac  = 0.05
)

func NewTracker() *Tracker { return &Tracker{m: map[Key]Trust{}} }

// Get is the last verdict for k (zero Trust = untrusted, never measured).
func (t *Tracker) Get(k Key) (Trust, bool) {
	tr, ok := t.m[k]
	return tr, ok
}

// Update re-measures when due. It returns the current verdict and whether a new
// measurement was taken (the caller logs it).
func (t *Tracker) Update(k Key, live, atlas, prior *Layer) (Trust, bool) {
	prev, seen := t.m[k]
	known := knownOver(live, atlas)
	if seen {
		grown := known - prev.Known
		if grown < remeasureCells && float64(grown) < remeasureFrac*float64(prev.Known) {
			return prev, false
		}
	}
	tr := Measure(live, atlas, prior)
	t.m[k] = tr
	return tr, true
}

// knownOver counts cells observed by live or atlas inside live's frame.
func knownOver(live, atlas *Layer) int {
	if live == nil {
		return 0
	}
	n := 0
	for ly := 0; ly < live.H; ly++ {
		for lx := 0; lx < live.W; lx++ {
			if live.Cells[ly*live.W+lx] != Unknown || atlas.At(lx+live.OffX, ly+live.OffY) != Unknown {
				n++
			}
		}
	}
	return n
}
