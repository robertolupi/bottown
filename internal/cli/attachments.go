package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/robertolupi/bottown/internal/runs"
	"github.com/robertolupi/bottown/internal/server"
)

// Issue attachments for harnesses: the server lists them with the issue
// and streams each one's bytes with the actor's forge token, within the
// key's scope; the CLI writes them into the run's attachments directory
// (runs/attachments.go) and tells the harness their paths.

// attachmentList is what the server listed for an issue, not yet fetched.
func attachmentList(is server.IssueOut) []runs.Attachment {
	out := make([]runs.Attachment, 0, len(is.Attachments))
	for _, a := range is.Attachments {
		out = append(out, runs.Attachment{ID: a.ID, Name: a.Name, Size: a.Size, Comment: a.Comment})
	}
	return out
}

// fetchAttachments downloads issue is's attachments into dir.
func (a *agentCtx) fetchAttachments(ctx context.Context, dir, repo string, is server.IssueOut) []runs.Attachment {
	return runs.FetchAttachments(ctx, dir, attachmentList(is), func(ctx context.Context, id int64) (io.ReadCloser, string, error) {
		return a.openAttachment(ctx, repo, is.Number, id)
	})
}

// openAttachment streams attachment id of issue n in repo (empty: the
// run's) from the bottown server.
func (a *agentCtx) openAttachment(ctx context.Context, repo string, n, id int64) (io.ReadCloser, string, error) {
	path := "/v1/forge/issue/attachment" + targetQuery(repo, n, url.Values{"id": {fmt.Sprint(id)}})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.ServerURL+path, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+a.Key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("bottown server unreachable: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		var e struct {
			Error string `json:"error"`
		}
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			return nil, "", errors.New(e.Error)
		}
		return nil, "", errors.New(resp.Status)
	}
	return resp.Body, resp.Header.Get("Content-Type"), nil
}

// issueShowJSON is `issue show --json`: the issue, with the attachments'
// local paths in place of the server's metadata.
type issueShowJSON struct {
	server.IssueOut
	Attachments []runs.Attachment `json:"attachments"`
}
