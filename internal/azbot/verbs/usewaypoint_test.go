package verbs

import (
	"testing"

	"github.com/hectorgimenez/d2go/pkg/data"
)

func TestPortalNearPadUsesPadNotPlayerGeometry(t *testing.T) {
	pad := data.Position{X: 100, Y: 100}
	portals := []data.Position{{X: 12, Y: 10}, {X: 113, Y: 100}}

	if portalNearPad(portals, pad, 12) {
		t.Fatal("portal 13 tiles from pad must not disable blind waypoint click")
	}
	portals[1] = data.Position{X: 112, Y: 100}
	if !portalNearPad(portals, pad, 12) {
		t.Fatal("portal overlapping pad neighborhood must disable blind click")
	}
}

func TestPortalNearPadEmpty(t *testing.T) {
	if portalNearPad(nil, data.Position{X: 100, Y: 100}, 12) {
		t.Fatal("empty portal set cannot threaten the waypoint pad")
	}
}
