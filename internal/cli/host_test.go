package cli

import "testing"

func TestOwnerEmail(t *testing.T) {
	for _, c := range []struct{ env, git, want string }{
		{"", "me@example.com", "me@example.com"},
		{"", "", ""},
		{"", "claude@bottown.local", ""},
		{"", "Kimi@Bottown.Local", ""},
		{"owner@e2e.test", "claude@bottown.local", "owner@e2e.test"},
		{"owner@e2e.test", "me@example.com", "owner@e2e.test"},
	} {
		if got := ownerEmail(c.env, c.git); got != c.want {
			t.Errorf("ownerEmail(%q, %q) = %q, want %q", c.env, c.git, got, c.want)
		}
	}
}
