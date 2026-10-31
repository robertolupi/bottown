package cli

import "testing"

func TestRepoFromGitURL(t *testing.T) {
	for _, tc := range []struct {
		url, org, repo string
		ok             bool
	}{
		{"http://127.0.0.1:3031/git/bottown/bottown.git", "bottown", "bottown", true},
		{"http://127.0.0.1:3041/git/bottown/demo", "bottown", "demo", true},
		{"http://127.0.0.1:3041/git/bottown/demo.git/", "bottown", "demo", true},
		{"https://github.com/robertolupi/bottown.git", "", "", false},
		{"http://127.0.0.1:3031/git/bottown", "", "", false},
		{"http://127.0.0.1:3031/git/bottown/a/b.git", "", "", false},
	} {
		org, repo, ok := repoFromGitURL(tc.url)
		if org != tc.org || repo != tc.repo || ok != tc.ok {
			t.Errorf("repoFromGitURL(%q) = %q, %q, %v; want %q, %q, %v", tc.url, org, repo, ok, tc.org, tc.repo, tc.ok)
		}
	}
}
