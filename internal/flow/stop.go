package flow

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The flow's comments on an issue start with commentPrefix and close the
// bold after a short head. A stop, "**bottown flow: <reason>**", is
// followed by a blank line and its detail; the flow's announcements of a
// round it starts ("**bottown flow: checks failed** at …") go on in the
// same line. ParseStop reads a stop back, for the activity derivation of
// the web UI (internal/activity), so the flow and its readers share one
// format. A stop ends with a marker line, stopMarker, that carries its
// reason for readers that do not parse the rest.
const commentPrefix = "**bottown flow: "

// StopReasons are the reasons a flow stops with.
var StopReasons = []StopReason{ReadyToMerge, OutOfRounds, RunFailed, AgentDidNothing, VerifierUndecided, ConflictsWithBase, ChangesRequested, ChecksFailed, ActorBusy, Merged}

// legacyConflictsWithMain is ConflictsWithBase's name before milestone
// bases, in the stops flows wrote then.
const legacyConflictsWithMain StopReason = "conflicts with main"

// stopBody is the issue comment for a stop.
func stopBody(reason StopReason, detail string, pr int64) string {
	body := commentPrefix + string(reason) + "**\n\n" + detail
	if pr > 0 {
		body += "\n\nPull request: !" + strconv.FormatInt(pr, 10)
	}
	return body + "\n\n" + stopMarker(reason)
}

// stopMarker is a stop comment's last line, hidden when rendered:
// <!-- bottown:flow-stop v=1 reason="agent did nothing" -->.
func stopMarker(reason StopReason) string {
	return fmt.Sprintf("<!-- bottown:flow-stop v=1 reason=%q -->", string(reason))
}

var stopMarkerRE = regexp.MustCompile(`\n*<!-- bottown:flow-stop v=1 reason="([a-z ]+)" -->\s*$`)

// roundBody is the issue comment announcing a round the flow starts.
func roundBody(head, text string) string {
	return commentPrefix + head + "** " + text
}

var stopPR = regexp.MustCompile(`\n\nPull request: !(\d+)\s*$`)

// Stop is a flow stop, read back from its issue comment.
type Stop struct {
	Reason StopReason
	// PR is the pull request the flow stopped on, 0 when it had none.
	PR int64
}

// ParseStop reads a stop comment that the flow wrote. It reports false for
// any other comment, the flow's announcements of a round included, and for
// a reason the flow does not know.
func ParseStop(body string) (Stop, bool) {
	rest, ok := strings.CutPrefix(body, commentPrefix)
	if !ok {
		return Stop{}, false
	}
	head, tail, ok := strings.Cut(rest, "**")
	if !ok || (tail != "" && !strings.HasPrefix(tail, "\n")) {
		return Stop{}, false
	}
	reason := StopReason(head)
	if reason == legacyConflictsWithMain {
		reason = ConflictsWithBase
	}
	if !slices.Contains(StopReasons, reason) {
		return Stop{}, false
	}
	stop := Stop{Reason: reason}
	if loc := stopMarkerRE.FindStringIndex(tail); loc != nil {
		tail = tail[:loc[0]]
	}
	if m := stopPR.FindStringSubmatch(tail); m != nil {
		stop.PR, _ = strconv.ParseInt(m[1], 10, 64)
	}
	return stop, true
}
