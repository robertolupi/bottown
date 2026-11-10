package webui

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"

	"github.com/robertolupi/bottown/internal/runs"
)

// logPage is GET /api/runs/{id}/log's answer (design §4, "Loading the
// log"). Stable is the length of the rendered log's stable prefix, and
// StableSum the digest of those lines (linesSum). With after=N and the
// digest of the N lines the page kept (sum=), Lines are the lines from
// position N on; Reset says they are the whole log instead, because the
// page's N lines are not the log's first N (it is rendered from another
// source now, or N is past its end), and the page discards what it had.
type logPage struct {
	ID        string         `json:"id"`
	Lines     []runs.LogLine `json:"lines"`
	Stable    int            `json:"stable"`
	StableSum string         `json:"stable_sum"`
	Reset     bool           `json:"reset,omitempty"`
}

// stablePrefix is the length of lines' stable prefix: the lines before the
// first one that can still change, a tool call still running or the last
// line of a live log, which the next write may extend. An ended run's log
// is stable as a whole. Unlike runs.LineTracker's final lines, the prefix
// stops at a running call: the lines after it are not final positions.
func stablePrefix(lines []runs.LogLine, ended bool) int {
	if ended {
		return len(lines)
	}
	for i, l := range lines {
		if l.Running() {
			return i
		}
	}
	return max(len(lines)-1, 0)
}

// linesSum is the digest of lines as the endpoint serves them.
func linesSum(lines []runs.LogLine) string {
	h := sha256.New()
	enc := json.NewEncoder(h)
	for _, l := range lines {
		_ = enc.Encode(l)
	}
	return hex.EncodeToString(h.Sum(nil)[:16])
}

var errBadAfter = errors.New("after must be a non-negative integer")

// pageLog answers a request for run id's rendered log, lines, from its
// query q: after and sum ask for the lines from a position on, tail (without
// after) for the last lines only. ended says the run has ended; when it is
// not known, false only holds back more lines.
func pageLog(id string, lines []runs.LogLine, ended bool, q url.Values) (logPage, error) {
	if lines == nil {
		lines = []runs.LogLine{}
	}
	stable := stablePrefix(lines, ended)
	p := logPage{ID: id, Lines: lines, Stable: stable, StableSum: linesSum(lines[:stable])}
	after := q.Get("after")
	if after == "" {
		if n, err := strconv.Atoi(q.Get("tail")); err == nil && n > 0 && len(lines) > n {
			p.Lines = lines[len(lines)-n:]
		}
		return p, nil
	}
	n, err := strconv.Atoi(after)
	if err != nil || n < 0 {
		return logPage{}, errBadAfter
	}
	sum := q.Get("sum")
	if sum == "" || n > len(lines) || linesSum(lines[:n]) != sum {
		p.Reset = true
		return p, nil
	}
	p.Lines = lines[n:]
	return p, nil
}
