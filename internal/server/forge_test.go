package server

import (
	"testing"

	sdk "codeberg.org/mvdkleijn/forgejo-sdk/forgejo/v3"
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
