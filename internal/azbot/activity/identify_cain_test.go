package activity

import (
	"testing"
	"time"
)

func TestCainIdentifyBidsOnlyInTownWithBacklog(t *testing.T) {
	defer cainIDWorks.Store(true)
	c := NewCainIdentify()
	now := time.Now()

	s := townSnap()
	if c.demand(s, now) != nil {
		t.Fatal("no unidentified items: must not bid")
	}
	s.Me.UnidentCount = 3
	s.Me.IDScrolls = 0 // Cain needs no tome charges
	if d := c.demand(s, now); d == nil || d.Who != "identify" {
		t.Fatalf("town + backlog + empty tome: want an identify bid, got %+v", d)
	}
	s.Me.InTown = false
	if c.demand(s, now) != nil {
		t.Fatal("outside town: must not bid")
	}
	s.Me.InTown = true
	cainIDWorks.Store(false)
	if c.demand(s, now) != nil {
		t.Fatal("retired belief: must not bid")
	}
	cainIDWorks.Store(true)
	c.coolAt = now.Add(time.Minute)
	if c.demand(s, now) != nil {
		t.Fatal("cooling down: must not bid")
	}
}

func TestServicesPendingCainIgnoresTomeCharges(t *testing.T) {
	s := townSnap()
	s.Me.UnidentCount = 2
	s.Me.IDScrolls = 0
	s.Me.TPScrolls = 10
	if !ServicesPending(s) {
		t.Fatal("unidentified items gate town even with an empty ID tome (Cain identifies)")
	}
}

func TestEmptyIDTomeIsNotAnErrand(t *testing.T) {
	s := townSnap()
	s.Me.IDScrolls = 0
	s.Me.TPScrolls = 10
	s.Me.Gold = 150000
	if why := ServicesPendingWhy(s); why == "scrolls" {
		t.Fatal("an empty ID tome must not hold the march (R15 idle deadlock)")
	}
}
