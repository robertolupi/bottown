package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/secrets"
)

const (
	defaultRunTTL = 12 * time.Hour
	maxRunTTL     = 48 * time.Hour
)

// RunRequest is the body of POST /v1/runs.
type RunRequest struct {
	Kind            string `json:"kind"` // implement | verify | discuss
	Actor           string `json:"actor"`
	Repo            string `json:"repo"` // name within the org
	Issue           int64  `json:"issue,omitempty"`
	PR              int64  `json:"pr,omitempty"`
	AllowSameFamily bool   `json:"allow_same_family,omitempty"`
	Interactive     bool   `json:"interactive,omitempty"`
	TTLSeconds      int64  `json:"ttl_seconds,omitempty"`
}

// RunResponse is a created run and its key. The key is returned once.
type RunResponse struct {
	secrets.Run
	Key string `json:"key"`
}

type runError struct {
	status int
	msg    string
}

func (e *runError) Error() string { return e.msg }

func runErr(status int, format string, args ...any) *runError {
	return &runError{status, fmt.Sprintf(format, args...)}
}

func (s *Server) postRun(w http.ResponseWriter, r *http.Request) {
	var req RunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	p := Who(r.Context())
	if p.Actor != s.Owner {
		// An actor starting a run for itself, e.g. /bottown-implement 12.
		switch {
		case s.desired().DisableActorSelfStart:
			writeError(w, http.StatusForbidden, "actors may not start their own runs on this instance")
			return
		case p.Run != nil:
			writeError(w, http.StatusConflict, "this key belongs to run "+p.Run.ID+"; end it before starting another")
			return
		case req.Actor != "" && req.Actor != p.Actor:
			writeError(w, http.StatusForbidden, "an actor can only start runs for itself")
			return
		case req.AllowSameFamily:
			writeError(w, http.StatusForbidden, "only the owner can allow a same-family review")
			return
		}
		req.Actor = p.Actor
	}
	run, err := s.newRun(r, req)
	if err != nil {
		if re, ok := err.(*runError); ok {
			writeError(w, re.status, re.msg)
			return
		}
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	run.StartedBy = p.Actor
	key, err := s.Store.NewRun(run)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("run created", "run", run.ID, "kind", run.Kind, "actor", run.Actor, "repo", run.Org+"/"+run.Repo,
		"issue", run.Issue, "pr", run.PR, "sha", run.SHA, "allow_same_family", run.AllowSameFamily,
		"interactive", run.Interactive, "started_by", run.StartedBy)
	writeJSON(w, http.StatusCreated, RunResponse{Run: run, Key: key})
}

// newRun validates a request against the forge and builds the run record.
func (s *Server) newRun(r *http.Request, req RunRequest) (secrets.Run, error) {
	var run secrets.Run
	actor, ok := s.desired().Actor(req.Actor)
	if !ok {
		return run, runErr(http.StatusBadRequest, "unknown actor %q (actors: %s)", req.Actor, strings.Join(s.desired().ActorNames(), ", "))
	}
	ttl := defaultRunTTL
	if req.TTLSeconds > 0 {
		ttl = min(time.Duration(req.TTLSeconds)*time.Second, maxRunTTL)
	}
	now := time.Now().UTC()
	run = secrets.Run{
		ID: newRunID(), Kind: req.Kind, Actor: actor.Name, Family: actor.Family, Harness: actor.Harness,
		Org: s.desired().Org, Repo: req.Repo, Interactive: req.Interactive,
		Created: now, Expires: now.Add(ttl),
	}
	api := s.Rec.API(r.Context())
	repo, resp, err := api.GetRepo(run.Org, req.Repo)
	if forgejo.IsNotFound(resp, err) {
		return run, runErr(http.StatusNotFound, "repo %s/%s not found", run.Org, req.Repo)
	} else if err != nil {
		return run, err
	}

	switch req.Kind {
	case policy.Implement:
		if req.Issue <= 0 {
			return run, runErr(http.StatusBadRequest, "implement runs need an issue")
		}
		is, resp, err := api.GetIssue(run.Org, run.Repo, req.Issue)
		switch {
		case forgejo.IsNotFound(resp, err):
			return run, runErr(http.StatusNotFound, "issue #%d not found", req.Issue)
		case err != nil:
			return run, err
		case is.PullRequest != nil:
			return run, runErr(http.StatusBadRequest, "#%d is a pull request; implement runs take an issue", req.Issue)
		case is.State != "open":
			return run, runErr(http.StatusConflict, "issue #%d is %s", req.Issue, is.State)
		}
		run.Issue, run.Branch = req.Issue, fmt.Sprintf("issue/%d", req.Issue)

	case policy.Verify:
		if req.PR <= 0 {
			return run, runErr(http.StatusBadRequest, "verify runs need a pull request")
		}
		pr, resp, err := api.GetPullRequest(run.Org, run.Repo, req.PR)
		switch {
		case forgejo.IsNotFound(resp, err):
			return run, runErr(http.StatusNotFound, "pull request #%d not found", req.PR)
		case err != nil:
			return run, err
		case pr.HasMerged || pr.State != "open":
			return run, runErr(http.StatusConflict, "pull request #%d is not open", req.PR)
		case pr.Head == nil || pr.Head.Sha == "":
			return run, fmt.Errorf("pull request #%d has no head commit", req.PR)
		}
		if pr.Poster != nil {
			run.Author = pr.Poster.UserName
		}
		if run.Author == actor.Name {
			return run, runErr(http.StatusConflict, "%s wrote pull request #%d and cannot verify it", actor.Name, req.PR)
		}
		// The cross-family rule: a model reviewing its own family's work is
		// the weakest reviewer, so it needs an explicit owner override.
		if author, isActor := s.desired().Actor(run.Author); isActor && author.Family == actor.Family {
			if !req.AllowSameFamily {
				return run, runErr(http.StatusConflict,
					"%s and the author %s are both %s; pass allow_same_family (--allow-same-family) to override",
					actor.Name, author.Name, actor.Family)
			}
			run.AllowSameFamily = true
		}
		run.PR, run.SHA, run.Branch = req.PR, pr.Head.Sha, pr.Head.Ref

	case policy.Discuss:
		// Any issue or pull request, open or closed: discussing changes
		// nothing but the comments. The run reads the default branch's
		// current code, recorded as its SHA.
		if req.Issue <= 0 {
			return run, runErr(http.StatusBadRequest, "discuss runs need an issue or pull request number")
		}
		if _, resp, err := api.GetIssue(run.Org, run.Repo, req.Issue); forgejo.IsNotFound(resp, err) {
			return run, runErr(http.StatusNotFound, "issue #%d not found", req.Issue)
		} else if err != nil {
			return run, err
		}
		b, _, err := api.GetRepoBranch(run.Org, run.Repo, repo.DefaultBranch)
		if err != nil {
			return run, fmt.Errorf("default branch %s: %w", repo.DefaultBranch, err)
		}
		if b.Commit == nil || b.Commit.ID == "" {
			return run, fmt.Errorf("default branch %s has no commit", repo.DefaultBranch)
		}
		run.Issue, run.SHA = req.Issue, b.Commit.ID

	default:
		return run, runErr(http.StatusBadRequest, `kind must be %q, %q or %q`, policy.Implement, policy.Verify, policy.Discuss)
	}
	return run, nil
}

func newRunID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "r-" + hex.EncodeToString(b)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Read()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	runs := make([]secrets.Run, 0, len(st.Runs))
	for _, run := range st.Runs {
		if repo := r.URL.Query().Get("repo"); repo == "" || run.Repo == repo {
			runs = append(runs, run)
		}
	}
	slices.SortFunc(runs, func(a, b secrets.Run) int { return b.Created.Compare(a.Created) })
	writeJSON(w, http.StatusOK, runs)
}

// PurgeRequest asks to delete a repository's ended runs (POST
// /v1/runs/purge, owner only), except those in Keep.
type PurgeRequest struct {
	Repo        string    `json:"repo"`
	EndedBefore time.Time `json:"ended_before"`
	Keep        []string  `json:"keep,omitempty"`
}

// purgeRuns deletes ended runs of one repository that ended before
// EndedBefore. Runs that have not ended are never purged: the owner should
// see a stuck run before it disappears.
func (s *Server) purgeRuns(w http.ResponseWriter, r *http.Request) {
	var req PurgeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Repo == "" || req.EndedBefore.IsZero() {
		writeError(w, http.StatusBadRequest, "repo and ended_before are required")
		return
	}
	ids, err := s.Store.PurgeRuns(func(run secrets.Run) bool {
		return run.Repo == req.Repo && run.Ended.Before(req.EndedBefore) && !slices.Contains(req.Keep, run.ID)
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if ids == nil {
		ids = []string{}
	}
	s.Log.Info("runs purged", "repo", req.Repo, "count", len(ids))
	writeJSON(w, http.StatusOK, map[string]any{"purged": ids})
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	st, err := s.Store.Read()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	run, ok := st.Runs[r.PathValue("id")]
	if p := Who(r.Context()); !ok || (p.Actor != s.Owner && p.Actor != run.Actor) {
		writeError(w, http.StatusNotFound, "no such run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// endRun ends a run and deletes its key. Body: {"status": "succeeded" |
// "failed" | "revoked"} (default "revoked").
func (s *Server) endRun(w http.ResponseWriter, r *http.Request) {
	body := struct {
		Status string `json:"status"`
	}{Status: "revoked"}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if !slices.Contains([]string{"succeeded", "failed", "revoked"}, body.Status) {
		writeError(w, http.StatusBadRequest, "status must be succeeded, failed or revoked")
		return
	}
	// The owner ends any run; an actor only its own.
	if p := Who(r.Context()); p.Actor != s.Owner {
		st, err := s.Store.Read()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if run, ok := st.Runs[r.PathValue("id")]; !ok || run.Actor != p.Actor {
			writeError(w, http.StatusNotFound, "no such run")
			return
		}
	}
	run, ok, err := s.Store.EndRun(r.PathValue("id"), body.Status, time.Now().UTC())
	switch {
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	case !ok:
		writeError(w, http.StatusNotFound, "no such run")
	default:
		s.Log.Info("run ended", "run", run.ID, "status", run.Status)
		writeJSON(w, http.StatusOK, run)
	}
}
