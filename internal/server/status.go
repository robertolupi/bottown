package server

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/robertolupi/bottown/internal/forgejo"
	"github.com/robertolupi/bottown/internal/policy"
	"github.com/robertolupi/bottown/internal/secrets"
)

// Commit statuses from `bottown check`. Agents cannot write statuses through
// the API proxy (policy rule commit-status): the server writes them, as the
// run's actor, only on the commit the run works on and only under the
// bottown/check and bottown/e2e contexts. A status reports what the run's
// actor says it ran; it is not a verification.

// CheckNames are the check groups a run may report, as bottown/<name>.
var CheckNames = []string{"check", "e2e"}

// maxStatusDescription keeps descriptions within what Forgejo shows.
const maxStatusDescription = 255

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$|^[0-9a-f]{64}$`)

// CommitStatusIn is the input of check status.
type CommitStatusIn struct {
	SHA         string `json:"sha"`
	Check       string `json:"check"` // check | e2e
	State       string `json:"state"` // success | failure
	Description string `json:"description,omitempty"`
}

// CommitStatus is one commit status.
type CommitStatus struct {
	Context     string    `json:"context"`
	State       string    `json:"state"`
	Description string    `json:"description,omitempty"`
	Creator     string    `json:"creator"`
	Created     time.Time `json:"created"`
}

// opStatus records a `bottown check` result on the run's commit: a verify
// run's pinned SHA, or the head of an implement or plan run's branch on the
// forge (so the commit must be pushed first).
func (s *Server) opStatus(ctx context.Context, p secrets.Principal, in CommitStatusIn) (CommitStatus, error) {
	if err := requireRole(p, policy.Implement, policy.Verify, policy.Plan); err != nil {
		s.toolAudit(p, "", "check_status", "deny", err.Error())
		return CommitStatus{}, err
	}
	run := p.Run
	if !slices.Contains(CheckNames, in.Check) {
		return CommitStatus{}, ferr(http.StatusBadRequest, "check must be one of %s", strings.Join(CheckNames, ", "))
	}
	state := sdk.StatusState(in.State)
	if state != sdk.StatusSuccess && state != sdk.StatusFailure {
		return CommitStatus{}, ferr(http.StatusBadRequest, "state must be success or failure")
	}
	sha := strings.ToLower(strings.TrimSpace(in.SHA))
	if !fullSHA.MatchString(sha) {
		return CommitStatus{}, ferr(http.StatusBadRequest, "sha must be a full commit id")
	}
	c, err := s.actorClient(ctx, p.Actor)
	if err != nil {
		return CommitStatus{}, err
	}
	switch run.Kind {
	case policy.Verify:
		if sha != run.SHA {
			s.toolAudit(p, "", "check_status", "deny", "commit "+sha+" is not the run's "+run.SHA)
			return CommitStatus{}, ferr(http.StatusConflict, "%s is not the commit this run verifies (%s)", sha, run.SHA)
		}
	default:
		head, err := c.BranchHead(run.Org, run.Repo, run.Branch)
		if forgejo.IsRawNotFound(err) {
			return CommitStatus{}, ferr(http.StatusConflict, "%s is not on the forge yet: push HEAD first", run.Branch)
		} else if err != nil {
			return CommitStatus{}, err
		}
		if head != sha {
			s.toolAudit(p, "", "check_status", "deny", "commit "+sha+" is not the head of "+run.Branch)
			return CommitStatus{}, ferr(http.StatusConflict, "%s on the forge is at %s, not %s: push HEAD first", run.Branch, head, sha)
		}
	}
	desc := strings.TrimSpace(in.Description)
	if desc != "" {
		desc += " "
	}
	desc += fmt.Sprintf("(%s run %s)", run.Kind, run.ID)
	if r := []rune(desc); len(r) > maxStatusDescription {
		desc = string(r[:maxStatusDescription-1]) + "…"
	}
	st, _, err := c.CreateStatus(run.Org, run.Repo, sha, sdk.CreateStatusOption{State: state, Context: "bottown/" + in.Check, Description: desc})
	if err != nil {
		return CommitStatus{}, err
	}
	s.toolAudit(p, "", "check_status", "allow", fmt.Sprintf("bottown/%s %s at %s", in.Check, state, sha))
	return commitStatus(st), nil
}

func commitStatus(st *sdk.Status) CommitStatus {
	return CommitStatus{Context: st.Context, State: string(st.State), Description: st.Description, Creator: login(st.Creator), Created: st.Created}
}

// latestStatuses keeps the newest status per context and creator, ordered
// by context then creator: two runs' reports on one commit both show.
func latestStatuses(sts []*sdk.Status) []CommitStatus {
	type key struct{ context, creator string }
	latest := map[key]*sdk.Status{}
	for _, st := range sts {
		k := key{st.Context, login(st.Creator)}
		if cur, ok := latest[k]; !ok || st.Created.After(cur.Created) || st.Created.Equal(cur.Created) && st.ID > cur.ID {
			latest[k] = st
		}
	}
	out := make([]CommitStatus, 0, len(latest))
	for _, st := range latest {
		out = append(out, commitStatus(st))
	}
	slices.SortFunc(out, func(a, b CommitStatus) int {
		if c := strings.Compare(a.Context, b.Context); c != 0 {
			return c
		}
		return strings.Compare(a.Creator, b.Creator)
	})
	return out
}
