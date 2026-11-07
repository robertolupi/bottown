package server

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/secrets"
	"github.com/robertolupi/bottown/internal/wiki"
)

// Debates (#315): in a round, discuss runs, one per actor, answer the same
// question about an issue independently, each in its own wiki file; a
// consolidate run then merges the round's answers, and the store rewrites
// the debate's page from them (wiki.ReviewPage). A round is open while any
// of its discuss runs is active, complete once they have all ended, and
// consolidated once r<N>-consolidated.md exists. A new round starts only
// after the one before it is consolidated.
//
// The server places each run in its round when the run is created
// (placeInDebate), from the wiki and the runs it has recorded, and the
// wiki store keeps each run to its round's files (policy.DecideWikiRound).

// DebateRequest puts a discuss run in a debate's round, or names the debate
// a consolidate run consolidates.
type DebateRequest struct {
	// Slug names the debate in the wiki ([<date>-]<issue>-<slug>). Empty
	// is the issue's one debate (under Date) if it has one, else a slug of
	// the issue's title.
	Slug string `json:"slug,omitempty"`
	Date string `json:"date,omitempty"` // YYYY-MM-DD, or empty
	// Raw lets a new round's runs read earlier rounds' answers, not only
	// their consolidations.
	Raw bool `json:"raw,omitempty"`
	// Question is a new round's question; a run joining a round asks the
	// round's.
	Question string `json:"question,omitempty"`
}

// errDebateQuiet refuses a debate round's discuss run a write the round's
// other runs could read: a comment, a new issue.
var errDebateQuiet = ferr(http.StatusForbidden, "a debate round's discuss runs write only their answer, in the wiki: the round's other runs would read a comment or an issue (bottown policy rule %s)", policy.DebateQuiet)

// inDebateRound reports whether run is a discuss run answering in a debate
// round: such a run keeps off the issue's thread.
func inDebateRound(run *secrets.Run) bool {
	return run != nil && run.Kind == policy.Discuss && run.Debate != nil
}

// wikiRound is the run's round as the wiki rules see it, nil outside a
// debate.
func wikiRound(p secrets.Principal) *policy.WikiRound {
	if p.Run == nil || p.Run.Debate == nil {
		return nil
	}
	return &policy.WikiRound{
		Debate: p.Run.Debate.Name, Round: p.Run.Debate.Round, Actor: p.Run.Actor,
		Raw: p.Run.Debate.Raw, Consolidate: p.Run.Kind == policy.Consolidate,
	}
}

// debateState is what the server knows of one debate: its round files, and
// the runs it recorded in its rounds.
type debateState struct {
	name  string
	files map[int][]wiki.Round
	runs  []secrets.Run
}

func (d debateState) rounds() int {
	n := 0
	for r := range d.files {
		n = max(n, r)
	}
	for _, r := range d.runs {
		n = max(n, r.Debate.Round)
	}
	return n
}

func (d debateState) consolidated(n int) bool {
	return slices.ContainsFunc(d.files[n], func(r wiki.Round) bool { return r.Author == wiki.Consolidated })
}

func (d debateState) answered(n int, actor string) bool {
	return slices.ContainsFunc(d.files[n], func(r wiki.Round) bool { return r.Author == actor })
}

func (d debateState) answers(n int) int {
	c := 0
	for _, r := range d.files[n] {
		if r.Author != wiki.Consolidated {
			c++
		}
	}
	return c
}

// active are round n's runs of kind whose keys still work.
func (d debateState) active(n int, kind string, now time.Time) []secrets.Run {
	var out []secrets.Run
	for _, r := range d.runs {
		if r.Debate.Round == n && r.Kind == kind && r.State(now) == secrets.RunActive {
			out = append(out, r)
		}
	}
	return out
}

// settings is the question and Raw of round n, from its first run.
func (d debateState) settings(n int) (secrets.Debate, bool) {
	for _, r := range d.runs {
		if r.Debate.Round == n && r.Kind == policy.Discuss {
			return *r.Debate, true
		}
	}
	return secrets.Debate{}, false
}

// debateRuns are the recorded runs, live or ended, of repo's debates.
func debateRuns(st secrets.State, org, repo string) []secrets.Run {
	var out []secrets.Run
	for _, rs := range []map[string]secrets.Run{st.Runs, st.History} {
		for _, r := range rs {
			if r.Debate != nil && r.Org == org && r.Repo == repo {
				out = append(out, r)
			}
		}
	}
	slices.SortFunc(out, func(a, b secrets.Run) int { return a.Created.Compare(b.Created) })
	return out
}

// debateName picks the debate a request means: the slug's, when given;
// else the issue's one debate under the date, from its files or its runs;
// else a new one, slugged from the issue's title.
func debateName(tree wiki.Tree, recorded []secrets.Run, issue int64, date, slug, title string) (string, error) {
	if slug != "" {
		return wiki.Debate{Issue: issue, Date: date, Slug: slug}.Name(), nil
	}
	names := wiki.Debates(tree)
	for _, r := range recorded {
		names = append(names, r.Debate.Name)
	}
	var found []string
	for _, n := range names {
		if d, ok := wiki.ParseDebate(n); ok && d.Issue == issue && d.Date == date && !slices.Contains(found, n) {
			found = append(found, n)
		}
	}
	switch len(found) {
	case 0:
		return wiki.Debate{Issue: issue, Date: date, Slug: wiki.Slug(title)}.Name(), nil
	case 1:
		return found[0], nil
	}
	slices.Sort(found)
	return "", runErr(http.StatusConflict, "#%d has several debates (%s): name one with its slug (--wiki-slug)", issue, strings.Join(found, ", "))
}

// placeInDebate puts a new discuss or consolidate run in its debate's
// round, or refuses it: a discuss run joins the open round, or starts the
// next one once the last is consolidated; a consolidate run takes the last
// round once it is complete. title is the issue's, for a new debate's slug.
func (s *Server) placeInDebate(ctx context.Context, run *secrets.Run, req RunRequest, title string) error {
	dr := *req.Debate
	if dr.Date != "" {
		if _, err := time.Parse(time.DateOnly, dr.Date); err != nil {
			return runErr(http.StatusBadRequest, "debate date %q is not YYYY-MM-DD", dr.Date)
		}
	}
	if err := wiki.ValidSlug(dr.Slug); err != nil {
		return runErr(http.StatusBadRequest, "%v", err)
	}
	ws, err := s.readWiki(ctx, run.Org, run.Repo)
	if err != nil {
		return err
	}
	st, err := s.Store.Read()
	if err != nil {
		return err
	}
	all := debateRuns(st, run.Org, run.Repo)
	name, err := debateName(ws.tree, all, run.Issue, dr.Date, dr.Slug, title)
	if err != nil {
		return err
	}
	d := debateState{name: name, files: wiki.Rounds(ws.tree, name)}
	for _, r := range all {
		if r.Debate.Name == name {
			d.runs = append(d.runs, r)
		}
	}
	now := time.Now().UTC()
	last := d.rounds()
	question := strings.TrimSpace(dr.Question)
	switch run.Kind {
	case policy.Discuss:
		if last == 0 || d.consolidated(last) {
			run.Debate = &secrets.Debate{Name: name, Round: last + 1, Raw: dr.Raw, Question: question}
			return nil
		}
		// Join the round that is not consolidated yet.
		if c := d.active(last, policy.Consolidate, now); len(c) > 0 {
			return runErr(http.StatusConflict, "round %d of %s is being consolidated by run %s", last, name, c[0].ID)
		}
		if d.answered(last, run.Actor) {
			return runErr(http.StatusConflict, "%s has answered round %d of %s; consolidate it before the next round", run.Actor, last, name)
		}
		for _, r := range d.active(last, policy.Discuss, now) {
			if r.Actor == run.Actor {
				return runErr(http.StatusConflict, "%s is answering round %d of %s in run %s", run.Actor, last, name, r.ID)
			}
		}
		set, _ := d.settings(last)
		if question != "" && question != set.Question {
			return runErr(http.StatusConflict, "round %d of %s is open with another question; join it without one, or consolidate it first", last, name)
		}
		if dr.Raw && !set.Raw {
			return runErr(http.StatusConflict, "round %d of %s is open without earlier answers (raw); join it without --raw, or consolidate it first", last, name)
		}
		run.Debate = &secrets.Debate{Name: name, Round: last, Raw: set.Raw, Question: set.Question}
		return nil

	case policy.Consolidate:
		switch {
		case last == 0:
			return runErr(http.StatusNotFound, "%s has no rounds to consolidate", name)
		case d.consolidated(last):
			return runErr(http.StatusConflict, "round %d of %s is consolidated; start round %d with `bottown debate`", last, name, last+1)
		}
		if open := d.active(last, policy.Discuss, now); len(open) > 0 {
			ids := make([]string, len(open))
			for i, r := range open {
				ids[i] = r.ID + " (" + r.Actor + ")"
			}
			return runErr(http.StatusConflict, "round %d of %s is open: %s still answering", last, name, strings.Join(ids, ", "))
		}
		if c := d.active(last, policy.Consolidate, now); len(c) > 0 {
			return runErr(http.StatusConflict, "round %d of %s is being consolidated by run %s", last, name, c[0].ID)
		}
		if d.answers(last) == 0 {
			return runErr(http.StatusConflict, "round %d of %s has no answers to consolidate", last, name)
		}
		set, _ := d.settings(last)
		run.Debate = &secrets.Debate{Name: name, Round: last, Raw: set.Raw, Question: set.Question}
		return nil
	}
	return runErr(http.StatusBadRequest, "%s runs take no debate", run.Kind)
}

// stampRound is the content of a round file as the store commits it: the
// writer's text under bottown's frontmatter.
func stampRound(p secrets.Principal, r wiki.Round, content string, now time.Time) string {
	f := wiki.Frontmatter{Round: r.N, Actor: p.Actor, Date: now.UTC().Format(time.DateOnly)}
	if d, ok := wiki.ParseDebate(r.Debate); ok {
		f.Issue = d.Issue
	}
	if p.Run != nil {
		f.Model, f.Run, f.SHA = p.Run.Spec, p.Run.ID, p.Run.SHA
	}
	return wiki.Stamp(content, f)
}

// roundConflict refuses a write to an existing round file, or to an
// answer in a consolidated round: round files are write-once, and a
// consolidated round takes no more answers. The owner may delete one.
func roundConflict(st *wikiState, r wiki.Round, exists bool) error {
	switch {
	case exists:
		return &wikiConflict{fmt.Sprintf("%s exists: round files are written once", r.File())}
	case r.Author != wiki.Consolidated && st.consolidated(r.Debate, r.N):
		return &wikiConflict{fmt.Sprintf("round %d of %s is consolidated: it takes no more answers", r.N, r.Debate)}
	}
	return nil
}
