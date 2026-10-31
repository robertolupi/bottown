package forgejo

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// AdminClient performs site administration that the REST API cannot, by
// running the `forgejo admin` CLI inside the Forgejo container: creating the
// first admin, minting tokens for users whose password bottown does not hold
// (the token API requires the user's own basic auth), and resetting
// passwords. It is privileged by construction and only works in-container.
type AdminClient struct {
	Bin string // defaults to "forgejo"
}

func (a AdminClient) run(ctx context.Context, args ...string) (string, error) {
	bin := a.Bin
	if bin == "" {
		bin = "forgejo"
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"admin", "user"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("forgejo admin user %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()+stdout.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// CreateAdmin creates a site admin with the given password.
func (a AdminClient) CreateAdmin(ctx context.Context, username, email, password string) error {
	_, err := a.run(ctx, "create",
		"--username", username,
		"--email", email,
		"--password", password,
		"--admin",
		"--must-change-password=false")
	return err
}

// GenerateToken mints an access token for username and returns its value.
func (a AdminClient) GenerateToken(ctx context.Context, username, name string, scopes []string) (string, error) {
	out, err := a.run(ctx, "generate-access-token",
		"--username", username,
		"--token-name", name,
		"--scopes", strings.Join(scopes, ","),
		"--raw")
	if err != nil {
		return "", err
	}
	// --raw prints only the token, but tolerate log lines before it.
	lines := strings.Split(out, "\n")
	return strings.TrimSpace(lines[len(lines)-1]), nil
}

// ChangePassword sets username's password.
func (a AdminClient) ChangePassword(ctx context.Context, username, password string) error {
	_, err := a.run(ctx, "change-password",
		"--username", username,
		"--password", password,
		"--must-change-password=false")
	return err
}
