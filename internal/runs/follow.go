package runs

import (
	"context"
	"os"
	"strings"
	"time"
)

// FollowLogEvery is how often LogFollower.Follow re-reads a run's log.
const FollowLogEvery = time.Second

// LogFollower renders a run's log as it grows: the log and its live
// transcript (ReadLog), re-rendered (RenderLog) only when either file's
// size or modification time changed since the last read. Re-parsing the
// whole log each time keeps the lines exactly what RenderLog makes of it;
// at a few concurrent runs, that is cheap. The CLI's terminal tail uses it.
type LogFollower struct {
	Path    string // the run's <id>.log
	Harness string

	stamp logStamp
	lines []LogLine
}

type logStamp struct{ log, live fileStamp }

type fileStamp struct {
	ok   bool
	size int64
	mod  time.Time
}

func statFile(path string) fileStamp {
	fi, err := os.Stat(path)
	if err != nil {
		return fileStamp{}
	}
	return fileStamp{ok: true, size: fi.Size(), mod: fi.ModTime()}
}

func (s fileStamp) same(t fileStamp) bool {
	return s.ok == t.ok && s.size == t.size && s.mod.Equal(t.mod)
}

// Poll reads the log if it changed since the last Poll, and returns its
// lines and whether they were re-rendered. A log that does not exist yet
// has no lines.
func (f *LogFollower) Poll() ([]LogLine, bool) {
	st := logStamp{
		log:  statFile(f.Path),
		live: statFile(strings.TrimSuffix(f.Path, ".log") + LiveTranscriptSuffix),
	}
	// Stat before reading: a write in between shows as a change next time.
	if st.log.same(f.stamp.log) && st.live.same(f.stamp.live) {
		return f.lines, false
	}
	b, err := ReadLog(f.Path)
	if err != nil {
		return f.lines, false
	}
	f.stamp = st
	f.lines = RenderLog(f.Harness, b)
	return f.lines, true
}

// LineTracker splits a followed log's lines into those that became final
// and those still live. A line is final once nothing later in the log can
// change it: it is not a tool call still running, whose result updates it
// in place, nor the last line, which the next write may still extend (a
// kimi output run, a plain line without its newline yet). Once the run has
// ended, every line is final. LogLine has no ID: lines are matched by
// position, which RenderLog keeps stable as the log grows.
type LineTracker struct {
	handed []bool // by position: handed out as final
}

// Update returns the lines that became final since the last Update, each
// once and in log order, and the lines not handed out yet, which are
// still live. ended says the run has ended and its log is complete.
func (t *LineTracker) Update(lines []LogLine, ended bool) (final, live []LogLine) {
	for len(t.handed) < len(lines) {
		t.handed = append(t.handed, false)
	}
	for i, l := range lines {
		if t.handed[i] {
			continue
		}
		running := l.Kind == LineTool && l.State == "running"
		if ended || !running && i < len(lines)-1 {
			t.handed[i] = true
			final = append(final, l)
		} else {
			live = append(live, l)
		}
	}
	return final, live
}

// Follow polls the log every interval until ctx is done, and calls fn
// with its lines whenever they were re-rendered. It does not poll once
// more at the end: a caller that wants the final log, after the launcher
// has appended the transcript, calls Poll.
func (f *LogFollower) Follow(ctx context.Context, every time.Duration, fn func([]LogLine)) {
	if lines, changed := f.Poll(); changed {
		fn(lines)
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		if lines, changed := f.Poll(); changed {
			fn(lines)
		}
	}
}
