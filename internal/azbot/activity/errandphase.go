package activity

// errandPhase is the NPC errand's state (was a bare int). Ordered: the services
// test "at or past the menu" with >=.
type errandPhase uint8

const (
	erSeek     errandPhase = iota // walk the ring until the NPC loads
	erApproach                    // reach the 4..7 talk band
	erTalk                        // hover-confirm, bare click, wait for the menu byte
	erMenu                        // menu open: Home/Down/Enter toward Trade
	erAct                         // trade open (or selected): the owner acts
)

func (p errandPhase) String() string {
	switch p {
	case erSeek:
		return "Seek"
	case erApproach:
		return "Approach"
	case erTalk:
		return "Talk"
	case erMenu:
		return "Menu"
	case erAct:
		return "Act"
	}
	return "?"
}

// PhaseSink receives every phase.Phaser line (the executive traces them as
// "T L=phase"). nil = silent.
var PhaseSink func(line string)

func phaseLog(line string) {
	if PhaseSink != nil {
		PhaseSink(line)
	}
}

// to moves the errand's phase and logs the transition with its reason.
func (e *errand) to(p errandPhase, why string) {
	if e.ph.Log == nil {
		e.ph.Log = phaseLog
	}
	e.ph.To(p, why)
}

// PhaseName: the errand phase of each NPC service, for the state line.
func (r *Restock) PhaseName() string { return r.e.ph.Phase().String() }
func (fc *Fence) PhaseName() string  { return fc.e.ph.Phase().String() }
func (h *Heal) PhaseName() string    { return h.e.ph.Phase().String() }
func (rp *Repair) PhaseName() string { return rp.e.ph.Phase().String() }
