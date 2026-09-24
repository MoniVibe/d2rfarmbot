// Command strikereplay is the combat estimator's OFFLINE CHECK: it replays a
// relay's recorded strikes (run log verb=strike lines) against its flight
// recorder snapshots through combat/learn — the same Tracker, credit rule
// and Model the live Fight uses — and prints the resulting table, the leap
// splash histogram and the engagement summaries. Pure Go, no game:
//
//	go run ./cmd/strikereplay -log run.out.gz -flight flight.jsonl.gz
//
// Older runs (before the dynamic strike) log a strike when its VERDICT
// closes, not when it was issued: the issue time is estimated back from the
// verdict (deaf ≈ close − 1s window; hit ≈ close − 0.35s), and the flight
// recorder samples every ~1.4s, so short GetHit flinches between frames are
// missed — deaths (disappearance) survive the sampling, flinches mostly do not.
package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hectorgimenez/koolo/internal/azbot/combat/learn"
)

type frame struct {
	At time.Time
	Me struct {
		Pos          struct{ X, Y int }
		HPPct, MPPct int
	}
	Enemies []struct {
		ID   uint32
		Pos  struct{ X, Y int }
		Mode uint32
	}
}

type strike struct {
	closeAt, at time.Time
	skill       string
	target      uint32
	d           int
	result      string
}

func open(path string) (io.ReadCloser, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(path, ".gz") {
		return f, nil
	}
	z, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{z, f}, nil
}

var strikeRe = regexp.MustCompile(`^time=(\S+) .*verb=strike .*ev="skill=([^=]+) mouse=\w+ target=(\d+) d=(-?\d+) result=(\w+)`)

func skillOf(name string) string {
	switch strings.TrimSpace(name) {
	case "Leap Attack":
		return learn.Leap
	case "Double Swing":
		return learn.Swing
	case "Attack":
		return learn.Basic
	}
	return learn.Carnage // the left skill ("Concentrate" in the mod table, id 144)
}

func readStrikes(path string) ([]strike, error) {
	r, err := open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []strike
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<22)
	for sc.Scan() {
		m := strikeRe.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, m[1])
		if err != nil {
			continue
		}
		id, _ := strconv.Atoi(m[3])
		d, _ := strconv.Atoi(m[4])
		s := strike{closeAt: t, skill: skillOf(m[2]), target: uint32(id), d: d, result: m[5]}
		switch s.result {
		case "whiff":
			continue // never reached a monster: no strike to learn from
		case "deaf":
			s.at = t.Add(-time.Second)
		case "overkill":
			s.at = t.Add(-200 * time.Millisecond)
		default:
			s.at = t.Add(-350 * time.Millisecond)
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].at.Before(out[j].at) })
	return out, sc.Err()
}

func readFrames(path string) ([]frame, error) {
	r, err := open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []frame
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		b := sc.Bytes()
		if len(b) > 5 && strings.HasPrefix(string(b[:6]), `{"k":`) {
			continue // decision records
		}
		var f frame
		if json.Unmarshal(b, &f) == nil && !f.At.IsZero() {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out, sc.Err()
}

func mons(f frame) []learn.Mon {
	out := make([]learn.Mon, 0, len(f.Enemies))
	for _, e := range f.Enemies {
		if e.Pos.X == 0 && e.Pos.Y == 0 {
			continue
		}
		out = append(out, learn.Mon{ID: e.ID, P: learn.Pt{X: e.Pos.X, Y: e.Pos.Y}, Mode: e.Mode})
	}
	return out
}

func near(f frame) int {
	me := learn.Pt{X: f.Me.Pos.X, Y: f.Me.Pos.Y}
	n := 0
	for _, e := range f.Enemies {
		if learn.Cheb(me, learn.Pt{X: e.Pos.X, Y: e.Pos.Y}) <= learn.EngageRadius {
			n++
		}
	}
	return n
}

func main() {
	logPath := flag.String("log", "", "run log (.out or .out.gz) with verb=strike lines")
	flightPath := flag.String("flight", "", "flight recorder (.jsonl or .jsonl.gz)")
	maxLag := flag.Duration("maxlag", 1500*time.Millisecond, "the newest frame before a strike must be at most this old")
	flag.Parse()
	strikes, err := readStrikes(*logPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log:", err)
		os.Exit(1)
	}
	frames, err := readFrames(*flightPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "flight:", err)
		os.Exit(1)
	}
	l := learn.NewLearner(nil, "replay")
	verdicts := map[string][2]int{} // skill/d-bucket → hits, total (the log's own verdicts)
	var placed, skipped int
	var sums []learn.Summary
	fi := 0
	var last *frame
	step := func(until time.Time) {
		for fi < len(frames) && !frames[fi].At.After(until) {
			f := frames[fi]
			fi++
			last = &frames[fi-1]
			if _, sum := l.Observe(f.At, f.Me.HPPct, f.Me.MPPct, mons(f), near(f)); sum != nil {
				sums = append(sums, *sum)
			}
		}
	}
	for _, s := range strikes {
		step(s.at)
		k := fmt.Sprintf("%s/%s", s.skill, learn.DistNames[learn.DistBucket(s.d)])
		v := verdicts[k]
		v[1]++
		if s.result == "hit" {
			v[0]++
		}
		verdicts[k] = v
		if last == nil || s.at.Sub(last.At) > *maxLag {
			skipped++
			continue
		}
		var tpos *learn.Pt
		for _, e := range last.Enemies {
			if e.ID == s.target {
				tpos = &learn.Pt{X: e.Pos.X, Y: e.Pos.Y}
				break
			}
		}
		if tpos == nil {
			skipped++
			continue
		}
		placed++
		l.Strike(&learn.Strike{Skill: s.skill, At: s.at, Me: learn.Pt{X: last.Me.Pos.X, Y: last.Me.Pos.Y}, Aim: *tpos,
			Target: s.target, HP0: last.Me.HPPct, MP0: last.Me.MPPct}, mons(*last), near(*last))
	}
	step(time.Unix(1<<40, 0))
	for _, s := range l.T.Flush(time.Unix(1<<40, 0)) {
		l.M.Observe(s.Sample())
	}
	fmt.Printf("strikes: %d issued (whiffs dropped), %d placed on a frame, %d skipped (no frame within %s or target not in it); frames %d\n\n",
		len(strikes), placed, skipped, *maxLag, len(frames))
	fmt.Println("== estimator table (measured cells; est = shrunk toward the prior) ==")
	fmt.Print(l.M.Table())
	fmt.Println("\n== the log's own per-target verdicts (hit / issued) by skill and distance ==")
	var keys []string
	for k := range verdicts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := verdicts[k]
		fmt.Printf("%-16s %3d/%-3d %5.0f%%\n", k, v[0], v[1], 100*float64(v[0])/float64(v[1]))
	}
	fmt.Println("\n== " + l.AoELine())
	fmt.Printf("\n== engagements: %d\n", len(sums))
	for _, s := range sums {
		fmt.Println(s.String())
	}
}
