package main

import "testing"

// Relay R10: calibration ran twice at startup — the zero value's armed=false
// read the first armed tick as a flip. Seeded by the first observation, it
// calibrates once; a real flip after that still recalibrates.
func TestArmWatchCalibratesOnceAtStartup(t *testing.T) {
	var a armWatch // start() calibrated before the loop: nothing seeded yet
	if a.flipped(true) {
		t.Fatal("the first armed tick read as a flip: a second calibration")
	}
	if a.flipped(true) {
		t.Fatal("steady armed state read as a flip")
	}
	if !a.flipped(false) {
		t.Fatal("a real disarm went unseen")
	}
	a.seed(false) // recalibrated
	if a.flipped(false) {
		t.Fatal("flip after the recalibration's seed")
	}
}
