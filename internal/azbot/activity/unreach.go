package activity

import (
	"sync"
	"time"

	"github.com/hectorgimenez/d2go/pkg/data"
)

// Monsters proven unreachable (Stand's stalemate): read as walled by the
// executive for a while, so nothing keeps swinging at them.
var unreach = struct {
	sync.Mutex
	until map[data.UnitID]time.Time
}{until: map[data.UnitID]time.Time{}}

// MarkUnreachable benches these units' targeting for d.
func MarkUnreachable(ids []data.UnitID, d time.Duration) {
	unreach.Lock()
	defer unreach.Unlock()
	t := time.Now().Add(d)
	for _, id := range ids {
		unreach.until[id] = t
	}
}

// Unreachable reports a benched unit.
func Unreachable(id data.UnitID) bool {
	unreach.Lock()
	defer unreach.Unlock()
	t, ok := unreach.until[id]
	if ok && time.Now().After(t) {
		delete(unreach.until, id)
		return false
	}
	return ok
}
