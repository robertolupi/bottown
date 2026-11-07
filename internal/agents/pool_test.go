package agents

import (
	"slices"
	"strings"
	"testing"

	"github.com/robertolupi/bottown/internal/config"
)

var testActors = []config.Actor{
	{Name: "claude", Family: "anthropic"},
	{Name: "opus", Family: "anthropic"},
	{Name: "agy", Family: "google"},
	{Name: "kimi", Family: "moonshot"},
	{Name: "crush", Family: "deepseek"},
}

func TestParse(t *testing.T) {
	pool, err := Parse("claude=*,agy:gemini-3.8-flash-medium=1,kimi=2,crush=*", testActors)
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{Actor: "claude", Family: "anthropic", Limit: 0},
		{Actor: "agy", Model: "gemini-3.8-flash-medium", Family: "google", Limit: 1},
		{Actor: "kimi", Family: "moonshot", Limit: 2},
		{Actor: "crush", Family: "deepseek", Limit: 0},
	}
	if got := pool.Entries(); !slices.Equal(got, want) {
		t.Fatalf("entries = %#v, want %#v", got, want)
	}
	if got := pool.Entries()[1].String(); got != "agy:gemini-3.8-flash-medium" {
		t.Errorf("model-pinned flow actor = %q", got)
	}
}

func TestParseRefusals(t *testing.T) {
	tests := []struct {
		name  string
		spec  string
		entry string
		want  string
	}{
		{name: "empty spec", want: "empty"},
		{name: "empty entry", spec: "claude=1,,agy=1", entry: `""`, want: "actor[:model]=N"},
		{name: "missing separator", spec: "claude", entry: "claude", want: "actor[:model]=N"},
		{name: "missing actor", spec: "=1", entry: "=1", want: "actor[:model]=N"},
		{name: "missing model", spec: "claude:=1", entry: "claude:=1", want: "names no model"},
		{name: "missing limit", spec: "claude=", entry: "claude=", want: "actor[:model]=N"},
		{name: "zero", spec: "claude=0", entry: "claude=0", want: "positive integer"},
		{name: "negative", spec: "claude=-1", entry: "claude=-1", want: "positive integer"},
		{name: "not a number", spec: "claude=many", entry: "claude=many", want: "positive integer"},
		{name: "unknown actor", spec: "nobody=1", entry: "nobody=1", want: `unknown actor "nobody"`},
		{name: "duplicate", spec: "claude=1,claude=2,agy=1", entry: "claude=2", want: "appears more than once"},
		{name: "duplicate with models", spec: "claude:one=1,claude:two=1,agy=1", entry: "claude:two=1", want: "appears more than once"},
		{name: "one family", spec: "claude=1,opus=2", want: `family "anthropic" cannot be verified`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.spec, testActors)
			if err == nil {
				t.Fatal("Parse succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
			if tt.entry != "" && !strings.Contains(err.Error(), tt.entry) {
				t.Errorf("error = %q, want it to name entry %q", err, tt.entry)
			}
		})
	}
}

func mustPool(t *testing.T, spec string) *Pool {
	t.Helper()
	pool, err := Parse(spec, testActors)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func pairNames(pair Pair) string {
	return pair.Implement.Actor + "/" + pair.Verify.Actor
}

func TestPairRoundRobin(t *testing.T) {
	pool := mustPool(t, "claude=1,agy=1,kimi=1,crush=1")
	want := []string{"claude/agy", "agy/kimi", "kimi/crush", "crush/claude", "claude/agy"}
	for i, want := range want {
		pair, ok := pool.Next(nil)
		if !ok {
			t.Fatalf("pair %d: none", i)
		}
		if got := pairNames(pair); got != want {
			t.Errorf("pair %d = %s, want %s", i, got, want)
		}
	}
}

func TestPairSkipsSameFamily(t *testing.T) {
	pool := mustPool(t, "claude=1,opus=1,agy=1")
	want := []string{"claude/agy", "opus/agy", "agy/claude"}
	for i, want := range want {
		pair, ok := pool.Next(nil)
		if !ok {
			t.Fatalf("pair %d: none", i)
		}
		if pair.Implement.Family == pair.Verify.Family {
			t.Errorf("pair %d is same-family: %+v", i, pair)
		}
		if got := pairNames(pair); got != want {
			t.Errorf("pair %d = %s, want %s", i, got, want)
		}
	}
}

func TestPairRespectsLimits(t *testing.T) {
	pool := mustPool(t, "claude=1,agy=1,kimi=1")
	pair, ok := pool.Next(map[string]int{"claude": 1})
	if !ok || pairNames(pair) != "agy/kimi" {
		t.Fatalf("with claude at capacity, pair = %s, %v", pairNames(pair), ok)
	}
	if pair, ok := pool.Next(map[string]int{"claude": 1, "agy": 1}); ok {
		t.Fatalf("with both possible verifiers at capacity, pair = %+v", pair)
	}
	pair, ok = pool.Next(map[string]int{"claude": 1})
	if !ok || pairNames(pair) != "kimi/agy" {
		t.Errorf("after capacity frees, pair = %s, %v", pairNames(pair), ok)
	}
}

func TestPairUnlimited(t *testing.T) {
	pool := mustPool(t, "claude=*,agy=1")
	pair, ok := pool.Next(map[string]int{"claude": 1000})
	if !ok || pairNames(pair) != "claude/agy" {
		t.Fatalf("unlimited entry pair = %s, %v", pairNames(pair), ok)
	}
	if pair, ok := pool.Next(map[string]int{"claude": 1000, "agy": 1}); ok {
		t.Fatalf("limited verifier at capacity, pair = %+v", pair)
	}
}

func TestPairRemoval(t *testing.T) {
	pool := mustPool(t, "claude=1,agy=1,kimi=1")
	if pair, ok := pool.Next(nil); !ok || pairNames(pair) != "claude/agy" {
		t.Fatalf("initial pair = %s, %v", pairNames(pair), ok)
	}
	if !pool.Remove("agy") || pool.Remove("agy") {
		t.Fatal("removal did not report present then already removed")
	}
	if pair, ok := pool.Next(nil); !ok || pairNames(pair) != "kimi/claude" {
		t.Errorf("after removing agy, pair = %s, %v", pairNames(pair), ok)
	}
	pool.Remove("claude")
	if pair, ok := pool.Next(nil); ok {
		t.Errorf("one remaining entry paired: %+v", pair)
	}
	if pool.Remove("nobody") {
		t.Error("unknown actor reported removed")
	}
}

func TestEntriesReturnsCopy(t *testing.T) {
	pool := mustPool(t, "claude=1,agy=1")
	entries := pool.Entries()
	entries[0].Actor = "changed"
	if pool.Entries()[0].Actor != "claude" {
		t.Error("mutating Entries result changed the pool")
	}
}

func TestLookupAndSpare(t *testing.T) {
	pool := mustPool(t, "claude=1,agy:m=*")
	if e, ok := pool.Lookup("agy"); !ok || e.String() != "agy:m" {
		t.Errorf("Lookup(agy) = %+v, %v", e, ok)
	}
	if _, ok := pool.Lookup("kimi"); ok {
		t.Error("Lookup found an actor outside the pool")
	}
	if !pool.Spare("claude", nil) || pool.Spare("claude", map[string]int{"claude": 1}) || !pool.Spare("agy", map[string]int{"agy": 9}) {
		t.Error("Spare does not follow the limits")
	}
	pool.Remove("agy")
	if _, ok := pool.Lookup("agy"); ok || pool.Spare("agy", nil) {
		t.Error("a removed entry is still found")
	}
}
