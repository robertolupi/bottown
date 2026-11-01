package server

import (
	"context"
	"encoding/json"
	"fmt"
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
		for _, p := range booleanSchemaPaths(tool.Name, schema, false, tool.Name) {
			t.Errorf("%s: boolean schema at %s", tool.Name, p)
		}
	}
}

// booleanSchemaPaths returns the paths of boolean subschemas (what the Go
// SDK renders for an `any` field, and strict MCP clients reject). The
// additionalProperties keyword is exempt: false there is the SDK's idiom
// for structs and strict clients accept it. inProps marks keys that are
// property names, so a property merely named "additionalProperties" is not
// mistaken for the keyword.
func booleanSchemaPaths(key string, v any, inProps bool, path string) []string {
	switch v := v.(type) {
	case bool:
		if key != "additionalProperties" || inProps {
			return []string{path}
		}
	case map[string]any:
		var out []string
		for k, sub := range v {
			out = append(out, booleanSchemaPaths(k, sub, key == "properties", path+"."+k)...)
		}
		return out
	case []any:
		var out []string
		for i, sub := range v {
			out = append(out, booleanSchemaPaths(key, sub, inProps, fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	}
	return nil
}

func TestBooleanSchemaPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		schema map[string]any
		want   []string
	}{
		{
			name:   "additionalProperties keyword exempt",
			schema: map[string]any{"type": "object", "additionalProperties": false},
		},
		{
			name: "nested additionalProperties keyword exempt",
			schema: map[string]any{"properties": map[string]any{
				"foo": map[string]any{"type": "object", "additionalProperties": false},
			}},
		},
		{
			name: "property named additionalProperties flagged",
			schema: map[string]any{"properties": map[string]any{
				"additionalProperties": true,
			}},
			want: []string{"s.properties.additionalProperties"},
		},
		{
			name: "boolean property schema flagged",
			schema: map[string]any{"properties": map[string]any{
				"body": true,
			}},
			want: []string{"s.properties.body"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := booleanSchemaPaths("s", tc.schema, false, "s")
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Errorf("booleanSchemaPaths() = %v, want %v", got, tc.want)
			}
		})
	}
}
