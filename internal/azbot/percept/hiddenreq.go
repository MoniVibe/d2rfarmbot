// hiddenreq.go — the gear oracle's TABLE PRIOR for hidden requirements
// (P-9.1, the owner: "can we have gear oracle know what can and cannot be
// equipped?"). A unique's real level gate lives in its uniqueitems.txt row —
// not on the base item, and not in any stat this repack populates (measured
// 10:11-10:18: a unique armor docketed past every memory-readable gate and
// burned three refusals learning what the table knew all along). The mod's
// own excel is the same source farmbot's gear oracle drank from; d2go's
// UniqueSetID is the row key. Fail-soft: with no table, the refusal oracle
// (P-4.4) still guards — this prior just saves her the wasted clicks.
package percept

import (
	"os"
	"strconv"
	"strings"
	"sync"
)

const modExcelDir = `C:\Program Files (x86)\Diablo II Resurrected\mods\D2RMM\D2RMM.mpq\data\global\excel\`

var (
	hiddenReqOnce sync.Once
	uniqueLvlReq  map[int]int // uniqueitems.txt *ID → lvl req
	setLvlReq     map[int]int // setitems.txt   *ID → lvl req
)

func loadHiddenReqs() {
	uniqueLvlReq = parseReqTable(modExcelDir+"uniqueitems.txt", "*id", "lvl req")
	setLvlReq = parseReqTable(modExcelDir+"setitems.txt", "*id", "lvl req")
}

// parseReqTable reads one tab-separated excel table into id → lvl-req.
// Columns are found by header name — the mod may reorder them.
func parseReqTable(path, idCol, reqCol string) map[int]int {
	m := map[int]int{}
	b, err := os.ReadFile(path)
	if err != nil {
		return m // fail-soft (P-4.4 guards live)
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) < 2 {
		return m
	}
	hdr := strings.Split(strings.TrimRight(lines[0], "\r"), "\t")
	idIdx, reqIdx := -1, -1
	for i, h := range hdr {
		switch strings.ToLower(strings.TrimSpace(h)) {
		case idCol:
			idIdx = i
		case reqCol:
			reqIdx = i
		}
	}
	if idIdx < 0 || reqIdx < 0 {
		return m
	}
	for _, ln := range lines[1:] {
		f := strings.Split(strings.TrimRight(ln, "\r"), "\t")
		if len(f) <= idIdx || len(f) <= reqIdx {
			continue
		}
		id, err1 := strconv.Atoi(strings.TrimSpace(f[idIdx]))
		rq, err2 := strconv.Atoi(strings.TrimSpace(f[reqIdx]))
		if err1 == nil && err2 == nil && rq > 0 {
			m[id] = rq
		}
	}
	return m
}
