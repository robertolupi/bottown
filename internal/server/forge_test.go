package server

import (
	"strings"
	"testing"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
	"github.com/robertolupi/bottown/internal/config"
	"github.com/robertolupi/bottown/internal/provision"
	"github.com/robertolupi/bottown/internal/secrets"
)

func TestParseState(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want sdk.StateType
		err  bool
	}{
		{"", sdk.StateOpen, false},
		{"open", sdk.StateOpen, false},
		{"OPEN", sdk.StateOpen, false},
		{"closed", sdk.StateClosed, false},
		{"CLOSED", sdk.StateClosed, false},
		{"all", sdk.StateAll, false},
		{"ALL", sdk.StateAll, false},
		{"invalid", "", true},
	} {
		got, err := parseState(tc.in)
		if (err != nil) != tc.err {
			t.Errorf("parseState(%q) err = %v, wantErr = %v", tc.in, err, tc.err)
		}
		if got != tc.want {
			t.Errorf("parseState(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestReviewState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reviews []*sdk.PullReview
		want    string
	}{
		{
			name:    "no reviews",
			reviews: nil,
			want:    "needs review",
		},
		{
			name: "only comments",
			reviews: []*sdk.PullReview{
				{State: sdk.ReviewStateComment},
			},
			want: "needs review",
		},
		{
			name: "approved",
			reviews: []*sdk.PullReview{
				{State: sdk.ReviewStateApproved},
			},
			want: "approved",
		},
		{
			name: "changes requested",
			reviews: []*sdk.PullReview{
				{State: sdk.ReviewStateRequestChanges},
			},
			want: "changes requested",
		},
		{
			name: "approved but also changes requested",
			reviews: []*sdk.PullReview{
				{State: sdk.ReviewStateApproved},
				{State: sdk.ReviewStateRequestChanges},
			},
			want: "changes requested",
		},
		{
			name: "stale or dismissed reviews ignored",
			reviews: []*sdk.PullReview{
				{State: sdk.ReviewStateRequestChanges, Stale: true},
				{State: sdk.ReviewStateApproved, Dismissed: true},
			},
			want: "needs review",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := reviewState(tc.reviews)
			if got != tc.want {
				t.Errorf("reviewState() = %q, want %q", got, tc.want)
			}
		})
	}
}

func testServer() *Server {
	return &Server{
		Rec: &provision.Reconciler{
			Desired: config.Desired{Org: "bottown"},
		},
	}
}

func TestResolveRepo(t *testing.T) {
	s := testServer()
	runP := secrets.Principal{
		Run: &secrets.Run{Org: "bottown", Repo: "demo"},
	}
	noRunP := secrets.Principal{}

	for _, tc := range []struct {
		name    string
		p       secrets.Principal
		repo    string
		wantOrg string
		wantRep string
		errSub  string
	}{
		// Inside run
		{name: "in run empty repo defaults", p: runP, repo: "", wantOrg: "bottown", wantRep: "demo"},
		{name: "in run matching repo", p: runP, repo: "demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "in run matching org/repo", p: runP, repo: "bottown/demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "in run matching BOTTOWN/demo", p: runP, repo: "BOTTOWN/demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "in run other repo forbidden", p: runP, repo: "other", errSub: "this run is confined to bottown/demo"},
		{name: "in run other org/repo forbidden", p: runP, repo: "bottown/other", errSub: "this run is confined to bottown/demo"},
		{name: "in run foreign org forbidden", p: runP, repo: "foreign/demo", errSub: "this run is confined to bottown/demo"},

		// Outside run
		{name: "no run empty repo rejected", p: noRunP, repo: "", errSub: "repo is required without a run"},
		{name: "no run plain repo", p: noRunP, repo: "demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "no run org/repo", p: noRunP, repo: "bottown/demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "no run case-insensitive org/repo", p: noRunP, repo: "Bottown/demo", wantOrg: "bottown", wantRep: "demo"},
		{name: "no run foreign org not found", p: noRunP, repo: "foreign/demo", errSub: "repository foreign/demo not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			org, repo, err := s.resolveRepo(tc.p, tc.repo)
			if tc.errSub != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errSub) {
					t.Fatalf("expected error containing %q, got: %v", tc.errSub, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if org != tc.wantOrg || repo != tc.wantRep {
				t.Errorf("resolveRepo() = (%q, %q), want (%q, %q)", org, repo, tc.wantOrg, tc.wantRep)
			}
		})
	}
}

func TestResolveTarget(t *testing.T) {
	s := testServer()
	runP := secrets.Principal{
		Run: &secrets.Run{Org: "bottown", Repo: "demo"},
	}
	noRunP := secrets.Principal{}

	// Inside run default number
	org, repo, n, err := s.resolve(runP, Target{Number: 0}, 42)
	if err != nil || org != "bottown" || repo != "demo" || n != 42 {
		t.Errorf("in run def: got (%s, %s, %d, %v), want (bottown, demo, 42, nil)", org, repo, n, err)
	}

	// Inside run explicit number and org/repo
	org, repo, n, err = s.resolve(runP, Target{Repo: "bottown/demo", Number: 10}, 42)
	if err != nil || org != "bottown" || repo != "demo" || n != 10 {
		t.Errorf("in run explicit: got (%s, %s, %d, %v), want (bottown, demo, 10, nil)", org, repo, n, err)
	}

	// Outside run missing repo
	_, _, _, err = s.resolve(noRunP, Target{Number: 5}, 0)
	if err == nil || !strings.Contains(err.Error(), "repo is required without a run") {
		t.Errorf("outside run missing repo: want error, got: %v", err)
	}

	// Outside run missing number
	_, _, _, err = s.resolve(noRunP, Target{Repo: "demo", Number: 0}, 0)
	if err == nil || !strings.Contains(err.Error(), "number is required without a run") {
		t.Errorf("outside run missing number: want error, got: %v", err)
	}

	// Outside run with org/repo and number
	org, repo, n, err = s.resolve(noRunP, Target{Repo: "bottown/demo", Number: 15}, 0)
	if err != nil || org != "bottown" || repo != "demo" || n != 15 {
		t.Errorf("outside run valid: got (%s, %s, %d, %v), want (bottown, demo, 15, nil)", org, repo, n, err)
	}
}
