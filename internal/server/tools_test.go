package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// kimi's MCP client strictly validates every tool's output schema: it must
// be a plain object, and boolean subschemas (what the Go SDK renders for an
// `any` field) are rejected.
func TestToolOutputSchemas(t *testing.T) {
	s := testServer()
	srv := s.mcpServer()

	ctx := context.Background()
	ct, st := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools listed")
	}
	for _, tool := range res.Tools {
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatalf("%s: marshal output schema: %v", tool.Name, err)
		}
		if string(raw) == "null" {
			// forge_api returns whatever the endpoint returns; it
			// deliberately advertises no output schema.
			if tool.Name != "forge_api" {
				t.Errorf("%s: no output schema", tool.Name)
			}
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Errorf("%s: output schema is not a JSON object: %v", tool.Name, err)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("%s: output schema type = %v, want object", tool.Name, schema["type"])
		}
		var walk func(v any, path string)
		walk = func(v any, path string) {
			switch v := v.(type) {
			case bool:
				// additionalProperties: false is the Go SDK's idiom for
				// structs and strict clients accept it; a boolean anywhere
				// else is the `any` rendering that broke kimi.
				if !strings.HasSuffix(path, ".additionalProperties") {
					t.Errorf("%s: boolean schema at %s", tool.Name, path)
				}
			case map[string]any:
				for k, sub := range v {
					walk(sub, path+"."+k)
				}
			case []any:
				for i, sub := range v {
					walk(sub, fmt.Sprintf("%s[%d]", path, i))
				}
			}
		}
		walk(schema, tool.Name)
	}
}
