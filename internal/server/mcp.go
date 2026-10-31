package server

import (
	"context"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/robertolupi/bottown/internal/secrets"
)

// WhoamiOut is the result of the whoami tool.
type WhoamiOut struct {
	Actor    string       `json:"actor"`
	Family   string       `json:"family,omitempty"` // model vendor; empty for the owner
	Instance string       `json:"instance"`
	Run      *secrets.Run `json:"run,omitempty"` // the run this key is scoped to, if any
}

// StatusOut is the result of the forge_status tool.
type StatusOut struct {
	Instance       string `json:"instance"`
	Version        string `json:"version"`
	ForgejoVersion string `json:"forgejo_version"`
	Reconciled     bool   `json:"reconciled"`
	Org            string `json:"org"`
}

func (s *Server) mcpServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "bottown", Version: s.Version}, nil)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "whoami",
		Description: "Report which bottown actor this connection is authenticated as.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, WhoamiOut, error) {
		p, _ := req.Extra.TokenInfo.Extra["principal"].(secrets.Principal)
		a, _ := s.desired().Actor(p.Actor)
		return nil, WhoamiOut{Actor: p.Actor, Family: a.Family, Instance: s.Instance, Run: p.Run}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "forge_status",
		Description: "Report the bottown instance, its Forgejo version and whether provisioning has converged.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, StatusOut, error) {
		h := s.healthSnapshot()
		return nil, StatusOut{
			Instance: h.Instance, Version: h.Version, ForgejoVersion: h.ForgejoVersion,
			Reconciled: h.Reconciled, Org: s.desired().Org,
		}, nil
	})
	return srv
}

func (s *Server) mcpHandler() http.Handler {
	srv := s.mcpServer()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	verify := func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		p, ok := s.Lookup(token)
		if !ok {
			return nil, auth.ErrInvalidToken
		}
		exp := time.Now().Add(time.Hour)
		if p.Run != nil {
			exp = p.Run.Expires
		}
		// UserID binds MCP sessions to the principal that opened them; a run
		// key's session is bound to its run.
		id := p.Actor
		if p.Run != nil {
			id += "/" + p.Run.ID
		}
		return &auth.TokenInfo{UserID: id, Expiration: exp, Extra: map[string]any{"principal": p}}, nil
	}
	return auth.RequireBearerToken(verify, nil)(h)
}
