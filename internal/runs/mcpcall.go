package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearer struct{ key string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.key)
	return http.DefaultTransport.RoundTrip(r)
}

// CallTool calls one bottown MCP tool and writes its structured result as
// JSON to out. A tool error is returned as an error.
func CallTool(ctx context.Context, url, key, tool, args string, out io.Writer) error {
	var a map[string]any
	if strings.TrimSpace(args) != "" {
		if err := json.Unmarshal([]byte(args), &a); err != nil {
			return fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	cl := mcp.NewClient(&mcp.Implementation{Name: "bottown-cli", Version: "0"}, nil)
	sess, err := cl.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearer{key}}}, nil)
	if err != nil {
		return err
	}
	defer sess.Close()
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: a})
	if err != nil {
		return err
	}
	if res.IsError {
		var msg []string
		for _, c := range res.Content {
			if t, ok := c.(*mcp.TextContent); ok {
				msg = append(msg, t.Text)
			}
		}
		return errors.New(strings.Join(msg, "\n"))
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(res.StructuredContent)
}
