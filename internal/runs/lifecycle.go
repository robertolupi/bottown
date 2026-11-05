package runs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/robertolupi/bottown/internal/secrets"
)

// Mode is where a run works and who ends it.
type Mode int

const (
	// Launched is a run in an ephemeral worktree under the runs directory,
	// whose harness this process launches (`bottown run implement` and
	// the like). The launcher owns the record: it finishes it when the
	// harness exits, then ends the run on the server, retrying for
	// EndBudget. A run it could not end there, `run gc` revokes.
	Launched Mode = iota
	// Leased is a run in an actor's permanent worktree, worked by an
	// interactive agent that ends it itself (`bottown run start` and
	// `run end`). The record keeps the run key until the server confirms
	// the end, so a later run start or end can finish an interrupted start
	// or end (Recover). One agent works a worktree at a time: the record
	// this process holds is the worktree's.
	Leased
)

// EndBudget is how long a launcher keeps trying to end its run on the
// server, and EndBackoff the waits between its attempts.
var (
	EndBudget  = 2 * time.Minute
	EndBackoff = []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 30 * time.Second}
)

// RetryEnd calls end, each attempt bounded to 30s, until it succeeds,
// EndBackoff runs out or ctx is done, and returns its last error.
func RetryEnd(ctx context.Context, end func(context.Context) error) error {
	attempt := func() error {
		actx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		return end(actx)
	}
	err := attempt()
	for _, d := range EndBackoff {
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(d):
		}
		err = attempt()
	}
	return err
}

// Lifecycle is one run's steps once the server has created it, the same in
// both modes: save the record (Prepare), prepare the worktree, the prompt
// and the attachments (Prepare's setup), run (Running), finish (Finished)
// and end the run on the server (End). Its failure paths are here, once:
// a setup that fails ends the run as a startup failure (Abort), and a run
// whose start or end was interrupted is reconciled from its record
// (Recover).
type Lifecycle struct {
	Mode  Mode
	Store Store
	// EndRun ends run id on the server with status and digest and returns
	// the run as the server has it.
	EndRun func(ctx context.Context, id, status string, d secrets.Digest) (secrets.Run, error)
	// GetRun reads run id from the server; Recover needs it.
	GetRun func(ctx context.Context, id string) (secrets.Run, error)
	// Release removes a leased run's local state (its worktree's run
	// config, its attachments) once the server has ended the run, before
	// the record is final: a failure leaves the record live, for Recover
	// to retry.
	Release func(Record) error
	// Ended, if set, is called with a leased run's final record.
	Ended func(Record)

	rec   Record
	ended bool
}

// Record is the run's record as the lifecycle last wrote or read it.
func (l *Lifecycle) Record() Record { return l.rec }

// Resume takes over run rec, so that End can end it: one whose record an
// earlier Prepare saved, in another process (a leased run's `run end`), or
// one just created on the server, before Prepare (whose rec need only
// name it).
func (l *Lifecycle) Resume(rec Record) { l.rec = rec }

// abortStatus is what a failed startup ends the run as: a launched run
// failed; a leased one revoked, since its agent never got the prompt.
func (l *Lifecycle) abortStatus() string {
	if l.Mode == Leased {
		return "revoked"
	}
	return StatusFailed
}

// Prepare saves rec as preparing, then runs setup. The record comes first,
// so that the run is found even if this process dies during setup: by gc
// for a launched run, which then sees it dead, and by Recover for a leased
// one. If saving or setup fails, the run is aborted.
func (l *Lifecycle) Prepare(ctx context.Context, rec Record, setup func() error) error {
	rec.Status = StatusPreparing
	l.rec = rec
	save := l.Store.Create
	if l.Mode == Leased {
		// run.json is the worktree's latest run: the new one replaces it.
		save = l.Store.Save
	}
	if err := save(rec); err != nil {
		return l.Abort(ctx, err)
	}
	if err := setup(); err != nil {
		return l.Abort(ctx, err)
	}
	return nil
}

// Abort ends a run whose startup failed with cause, which it returns,
// joined with the error ending the run, if any. It runs even when ctx was
// cancelled: the end has its own bounds.
func (l *Lifecycle) Abort(ctx context.Context, cause error) error {
	err := l.End(context.WithoutCancel(ctx), l.abortStatus(), secrets.Digest{Failure: &secrets.Failure{
		Cause: secrets.FailureStartup, Detail: cause.Error()}})
	if err == nil {
		return cause
	}
	if l.Mode == Leased {
		err = fmt.Errorf("rollback run %s (retry run end): %w", l.rec.ID, err)
	}
	return errors.Join(cause, err)
}

// Running marks the run running, with change's other fields (its
// worktree, say): the agent has, or is about to get, its prompt.
func (l *Lifecycle) Running(change func(*Record)) error {
	return l.update(func(r *Record) error {
		r.Status = StatusRunning
		if change != nil {
			change(r)
		}
		return nil
	})
}

// Update changes the record (a launched run's harness pid, say).
func (l *Lifecycle) Update(change func(*Record) error) error { return l.update(change) }

// Finished records a launched run's local outcome: change sets its status
// and exit code; the run's end time is now, and its key is dropped, since
// the harness that used it is gone. It returns the record.
func (l *Lifecycle) Finished(change func(*Record)) (Record, error) {
	now := time.Now().UTC()
	err := l.update(func(r *Record) error {
		change(r)
		r.Ended, r.Key = &now, ""
		return nil
	})
	return l.rec, err
}

func (l *Lifecycle) update(change func(*Record) error) error {
	if l.Mode == Leased {
		r := l.rec
		if err := change(&r); err != nil {
			return err
		}
		if err := l.Store.Save(r); err != nil {
			return err
		}
		l.rec = r
		return nil
	}
	return l.Store.Update(func(r *Record) error {
		if err := change(r); err != nil {
			return err
		}
		l.rec = *r
		return nil
	})
}

// End ends the run on the server with status and digest.
//
// A launched run's record is finished first, if Finished has not done it,
// as status: the launcher is done with it, whatever the server says. A
// record that was never saved (a startup that failed before Prepare) has
// nothing to finish. Ending the run on the server is retried for
// EndBudget, once: a later End does nothing, even after a failure, which
// leaves the run to gc, as the error says.
//
// A leased run is ended first on the server, which must confirm it; then
// Release runs, and the record is final, without its key. If any step
// fails the record keeps its key, and a later End, or Recover in another
// process, finishes the end. Once it has succeeded, End does nothing.
func (l *Lifecycle) End(ctx context.Context, status string, d secrets.Digest) error {
	if l.ended {
		return nil
	}
	if l.Mode == Leased {
		ended, err := l.EndRun(ctx, l.rec.ID, status, d)
		if err != nil {
			return err
		}
		return l.finishLease(ended)
	}
	l.ended = true
	_ = l.update(func(r *Record) error {
		if r.Ended == nil {
			now := time.Now().UTC()
			r.Status, r.Ended, r.Key = status, &now, ""
		}
		return nil
	})
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), EndBudget)
	defer cancel()
	// The server may be restarting (an upgrade, say): retry for a while.
	if err := RetryEnd(cctx, func(ctx context.Context) error {
		_, err := l.EndRun(ctx, l.rec.ID, status, d)
		return err
	}); err != nil {
		return fmt.Errorf("end run %s: %w; the next `bottown run gc` revokes it", l.rec.ID, err)
	}
	return nil
}

// finishLease finalizes a leased run the server has ended.
func (l *Lifecycle) finishLease(ended secrets.Run) error {
	if ended.ID != l.rec.ID || ended.Actor != l.rec.Actor || ended.Ended == nil {
		return errors.New("server did not confirm this run ended")
	}
	if l.Release != nil {
		if err := l.Release(l.rec); err != nil {
			return err
		}
	}
	r := l.rec
	r.Status, r.Ended, r.Key = ended.Status, ended.Ended, ""
	if err := l.Store.Save(r); err != nil {
		return err
	}
	l.rec, l.ended = r, true
	if l.Ended != nil {
		l.Ended(r)
	}
	return nil
}

// Recover reconciles a leased run's record, rec, which still holds its key,
// with the server: a run the server has ended only needs its record
// finished; one whose startup was interrupted, while preparing, is
// revoked. It reports whether it did either. A running run is left alone:
// its agent ends it. The caller checks that rec is the worktree's run;
// Recover checks that the server's run is rec's.
func (l *Lifecycle) Recover(ctx context.Context, rec Record) (bool, error) {
	l.rec = rec
	remote, err := l.GetRun(ctx, rec.ID)
	if err != nil {
		return false, err
	}
	if remote.ID != rec.ID || remote.Actor != rec.Actor {
		return false, errors.New("server lease identity mismatch; refusing recovery")
	}
	if remote.Ended != nil {
		return true, l.finishLease(remote)
	}
	if rec.Status == StatusPreparing {
		return true, l.End(ctx, "revoked", secrets.Digest{Failure: &secrets.Failure{
			Cause: secrets.FailureStartup, Detail: "its startup was interrupted before the agent received the prompt"}})
	}
	return false, nil
}
