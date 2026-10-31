package server

import (
	"context"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// WhoamiOut is the result of the whoami tool.
type WhoamiOut struct {
	Actor    string `json:"actor"`
	Instance string `json:"instance"`
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
		return nil, WhoamiOut{Actor: req.Extra.TokenInfo.UserID, Instance: s.Instance}, nil
	})
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "forge_status",
		Description: "Report the bottown instance, its Forgejo version and whether provisioning has converged.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, StatusOut, error) {
		h := s.healthSnapshot()
		return nil, StatusOut{
			Instance: h.Instance, Version: h.Version, ForgejoVersion: h.ForgejoVersion,
			Reconciled: h.Reconciled, Org: s.Desired.Org,
		}, nil
	})
	return srv
}

func (s *Server) mcpHandler() http.Handler {
	srv := s.mcpServer()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	verify := func(ctx context.Context, token string, _ *http.Request) (*auth.TokenInfo, error) {
		actor, ok := s.LookupKey(token)
		if !ok {
			return nil, auth.ErrInvalidToken
		}
		// UserID binds MCP sessions to the actor that opened them.
		return &auth.TokenInfo{UserID: actor, Expiration: time.Now().Add(time.Hour)}, nil
	}
	return auth.RequireBearerToken(verify, nil)(h)
}
