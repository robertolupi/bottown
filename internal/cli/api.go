package cli

import (
	"context"
	"fmt"

	"github.com/robertolupi/bottown/internal/client"
	"github.com/robertolupi/bottown/internal/config"
)

// callServer makes an authenticated JSON request to the instance's bottown
// server. path starts with / (e.g. /v1/runs, /forgejo/api/v1/user). A
// refusal is a *client.APIError.
func callServer(ctx context.Context, inst config.Instance, key, method, path string, in, out any) error {
	return instanceErr(inst, client.New(inst.ServerURL(), key).Call(ctx, method, path, in, out))
}

// instanceErr names the instance in an error from a call to its server.
func instanceErr(inst config.Instance, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("instance %s: %w", inst.Name, err)
}
