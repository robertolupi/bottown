package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/robertolupi/bottown/internal/config"
)

// callServer makes an authenticated JSON request to the instance's bottown
// server. path starts with / (e.g. /v1/runs, /forgejo/api/v1/user).
func callServer(ctx context.Context, inst config.Instance, key, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, inst.ServerURL()+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("instance %s unreachable (is it up?): %w", inst.Name, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
		}
		if json.Unmarshal(raw, &e) == nil && (e.Error != "" || e.Message != "") {
			return fmt.Errorf("%s %s: %s%s", method, path, e.Error, e.Message)
		}
		return fmt.Errorf("%s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}
