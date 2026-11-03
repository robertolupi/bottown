package runs

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LiveTranscriptSuffix names a running batch run's live transcript: while
// a Transcriber's harness runs, the launcher keeps the session's transcript
// so far in <id>.transcript (FollowTranscript), and readers of the log
// (ReadLog) see it after the log, as it will be appended when the run ends.
const LiveTranscriptSuffix = ".transcript"

// FollowTranscriptEvery is how often FollowTranscript exports the session.
const FollowTranscriptEvery = 3 * time.Second

// FollowTranscript exports the run's transcript every interval until ctx
// is done, and replaces path with it, encoded payloads elided, when it
// changed. An export that fails (the harness has not started its session
// yet, say) leaves path as it was. It does not remove path: the launcher
// does, before it appends the final transcript to the log.
func FollowTranscript(ctx context.Context, t Transcriber, l Launch, since time.Time, path string, every time.Duration) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	var last []byte
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		// Bounded, so a stuck export cannot stall the next ones.
		ectx, cancel := context.WithTimeout(ctx, max(every, 30*time.Second))
		var b bytes.Buffer
		err := t.Transcript(ectx, l, since, &b)
		cancel()
		if err != nil || ctx.Err() != nil || bytes.Equal(b.Bytes(), last) {
			continue
		}
		if writeFileAtomic(path, RedactEncoded(b.Bytes())) == nil {
			last = b.Bytes()
		}
	}
}

// writeFileAtomic replaces path with b, 0600, so a reader sees the old
// file or the new one, never part of it.
func writeFileAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}

// ReadLog reads a run's log, logPath (<id>.log), followed by its live
// transcript, if the run is still writing one: what the log will hold
// when the run ends, so far.
func ReadLog(logPath string) ([]byte, error) {
	b, err := os.ReadFile(logPath)
	if err != nil {
		return nil, err
	}
	if live, err := os.ReadFile(strings.TrimSuffix(logPath, ".log") + LiveTranscriptSuffix); err == nil {
		b = append(b, live...)
	}
	return b, nil
}
