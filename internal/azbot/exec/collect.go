package exec

import "github.com/hectorgimenez/koolo/internal/azbot/arbiter"

// Collect gathers bids in registry order and stamps Order = index, so an exact
// tie resolves by registration — never by Go's map order.
func Collect[B any](bidders []B, bid func(B) *arbiter.Demand) []arbiter.Demand {
	var out []arbiter.Demand
	for i, b := range bidders {
		if d := bid(b); d != nil {
			dd := *d
			dd.Order = i
			out = append(out, dd)
		}
	}
	return out
}
