// Package review decides where a pull request's reviews leave it. The flow
// supervisor, the PR list summary and the owner inbox all read the same
// Assessment, so they cannot disagree about whether a PR is approved.
//
// The rules follow the forge's branch protection (required approvals from
// the allow-list, dismiss stale approvals, block on rejected reviews) and
// add bottown's cross-family rule on top; they never count a review the
// forge would not:
//
//   - Dismissed reviews never count.
//   - APPROVED and REQUEST_CHANGES count only when official. Forgejo keeps
//     one official verdict per reviewer, its latest, so a reviewer's new
//     verdict replaces its earlier one.
//   - An official, undismissed REQUEST_CHANGES blocks at any commit: the
//     forge keeps blocking the merge after new commits, until the reviewer
//     reviews again or the review is dismissed.
//   - An approval counts only at the current head and when not stale, and
//     only when it satisfies the cross-family rule: the reviewer is the
//     owner, an actor of another family than the author's, or an actor whose
//     verify run at that commit the owner allowed to be same-family.
//   - An official approval at an older head also counts, stale or not, when
//     the policy carries that head over: its patch is the current head's,
//     only rebased onto, or merged with, a newer base (SamePatch). The
//     cross-family rule applies to it as at its own commit.
//   - COMMENT reviews are never official in Forgejo. An actor's COMMENT at
//     the current head is an undecided verdict; the owner's is discussion.
//
// Precedence: a blocking request at the head, then a qualifying approval
// at the head, then a carried-over one (either is held back by a blocking
// request at an older head), then an actor's comment at the head, then
// nothing.
package review

import (
	"fmt"
	"time"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"

	"github.com/robertolupi/bottown/internal/secrets"
)

// Review states, as the forge reports them.
const (
	StateApproved       = "APPROVED"
	StateRequestChanges = "REQUEST_CHANGES"
	StateComment        = "COMMENT"
)

// Decision is where a PR's reviews leave it.
type Decision string

const (
	// NeedsReview: no verdict at the current head. The PR may still be
	// held back by a request for changes at an older head; a new verdict
	// from that reviewer replaces it.
	NeedsReview Decision = "needs review"
	// Approved: a qualifying approval at the head, and nothing blocks.
	Approved Decision = "approved"
	// ChangesRequested: a request for changes blocks the merge. It is at
	// the head, or at an older head while the head is approved.
	ChangesRequested Decision = "changes requested"
	// Commented: an actor left an undecided COMMENT verdict at the head.
	Commented Decision = "commented"
)

// Review is one review, in the fields the assessment reads.
type Review struct {
	ID        int64     `json:"id"`
	Reviewer  string    `json:"reviewer"`
	State     string    `json:"state"`
	CommitID  string    `json:"commit_id"`
	Stale     bool      `json:"stale"`
	Dismissed bool      `json:"dismissed"`
	Official  bool      `json:"official"`
	Submitted time.Time `json:"submitted,omitempty"`
}

// Grant is an owner-authorized same-family review: Reviewer's verify run
// at Commit.
type Grant struct {
	Reviewer string
	Commit   string
}

// Carry is an older head whose patch is the current head's: the same
// change, rebased onto or merged with Onto, the head's merge base.
type Carry struct {
	From string `json:"from"`
	Onto string `json:"onto"`
}

// Policy is who wrote the PR and who may approve it.
type Policy struct {
	Author   string
	Owner    string
	Families map[string]string // actor login -> model family
	Grants   []Grant
	// Carries are older heads whose approvals count at the head.
	Carries []Carry
}

// Assessment is the decision and the review it rests on: the newest
// blocking request, approval or comment behind it; nil for NeedsReview.
// Carried is set when the approval is at an older head carried over, and
// Reason then says so.
type Assessment struct {
	Decision Decision `json:"decision"`
	Review   *Review  `json:"review,omitempty"`
	Carried  *Carry   `json:"carried,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// AtHead reports whether the deciding review is at head. A
// ChangesRequested that is not at head is a request from an older head
// that still blocks an approved PR.
func (a Assessment) AtHead(head string) bool {
	return a.Review != nil && a.Review.CommitID == head && !a.Review.Stale
}

// Assess decides where reviews leave a PR whose current head is head.
func Assess(head string, reviews []Review, p Policy) Assessment {
	var blockingHead, blockingOld, approval, carried, comment *Review
	newest := func(cur *Review, r *Review) *Review {
		if cur == nil || r.ID > cur.ID {
			return r
		}
		return cur
	}
	for i := range reviews {
		r := &reviews[i]
		if r.Dismissed {
			continue
		}
		atHead := head != "" && r.CommitID == head && !r.Stale
		switch r.State {
		case StateRequestChanges:
			if !r.Official {
				continue
			}
			if atHead {
				blockingHead = newest(blockingHead, r)
			} else {
				blockingOld = newest(blockingOld, r)
			}
		case StateApproved:
			if !r.Official || !p.qualifies(*r) {
				continue
			}
			if atHead {
				approval = newest(approval, r)
			} else if p.carry(r.CommitID) != nil {
				carried = newest(carried, r)
			}
		case StateComment:
			if _, actor := p.Families[r.Reviewer]; actor && atHead {
				comment = newest(comment, r)
			}
		}
	}
	var carry *Carry
	if approval == nil && carried != nil {
		approval, carry = carried, p.carry(carried.CommitID)
	}
	switch {
	case blockingHead != nil:
		return Assessment{Decision: ChangesRequested, Review: copyOf(blockingHead)}
	case approval != nil && blockingOld != nil:
		return Assessment{Decision: ChangesRequested, Review: copyOf(blockingOld)}
	case approval != nil && carry != nil:
		c := *carry
		return Assessment{Decision: Approved, Review: copyOf(approval), Carried: &c,
			Reason: fmt.Sprintf("approval at %s carried over: same patch rebased onto %s", short(c.From), short(c.Onto))}
	case approval != nil:
		return Assessment{Decision: Approved, Review: copyOf(approval)}
	case comment != nil:
		return Assessment{Decision: Commented, Review: copyOf(comment)}
	}
	return Assessment{Decision: NeedsReview}
}

// carry returns the carry-over for commit, or nil.
func (p Policy) carry(commit string) *Carry {
	for i := range p.Carries {
		if commit != "" && p.Carries[i].From == commit {
			return &p.Carries[i]
		}
	}
	return nil
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// qualifies applies the cross-family rule to an approval.
func (p Policy) qualifies(r Review) bool {
	if p.Owner != "" && r.Reviewer == p.Owner {
		return true
	}
	family, actor := p.Families[r.Reviewer]
	if !actor || family == "" || r.Reviewer == p.Author {
		return false
	}
	if family != p.Families[p.Author] {
		return true
	}
	for _, g := range p.Grants {
		if g.Reviewer == r.Reviewer && g.Commit == r.CommitID {
			return true
		}
	}
	return false
}

func copyOf(r *Review) *Review {
	c := *r
	return &c
}

// Grants lists the owner-authorized same-family verify runs on pr among
// runs, the server's run records. Run records are written by the server
// when the owner starts the run, so unlike review bodies they can be
// trusted.
func Grants(runs []secrets.Run, repo string, pr int64) []Grant {
	var out []Grant
	for _, run := range runs {
		if run.Kind == "verify" && run.AllowSameFamily && run.Repo == repo && run.PR == pr && run.SHA != "" {
			out = append(out, Grant{Reviewer: run.Actor, Commit: run.SHA})
		}
	}
	return out
}

// FromForge converts the forge's reviews.
func FromForge(rs []*sdk.PullReview) []Review {
	out := make([]Review, 0, len(rs))
	for _, r := range rs {
		if r == nil {
			continue
		}
		reviewer := ""
		if r.Reviewer != nil {
			reviewer = r.Reviewer.UserName
		}
		out = append(out, Review{
			ID: r.ID, Reviewer: reviewer, State: string(r.State), CommitID: r.CommitID,
			Stale: r.Stale, Dismissed: r.Dismissed, Official: r.Official, Submitted: r.Submitted,
		})
	}
	return out
}
