package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultDesiredIsValid(t *testing.T) {
	d, err := LoadDesired("/nonexistent/bottown.toml")
	if err != nil {
		t.Fatal(err)
	}
	if d.Manager.Name != "manager" {
		t.Fatalf("manager = %+v", d.Manager)
	}
	if _, ok := d.Actor("manager"); ok {
		t.Fatal("manager must not be an actor (actors can get API keys)")
	}
	if a, ok := d.Actor("claude"); !ok || a.Family != "anthropic" || a.BatchRunLimit() != 1 {
		t.Fatalf("claude = %+v", a)
	}
}

func TestActorBatchRunLimit(t *testing.T) {
	zero := 0
	if got := (Actor{MaxBatchRuns: &zero}).BatchRunLimit(); got != 0 {
		t.Fatalf("explicit zero = %d", got)
	}
	negative := -1
	d := DefaultDesired()
	d.Actors[0].MaxBatchRuns = &negative
	if err := d.validate(); err == nil {
		t.Fatal("negative max_batch_runs accepted")
	}
}

// The host's [actor_models] table names default models; saving the
// instance (as `bottown up` does) keeps it.
func TestActorModels(t *testing.T) {
	t.Setenv("BOTTOWN_HOME", t.TempDir())
	dir, _ := InstanceDir("dev")
	os.MkdirAll(dir, 0o700)
	os.WriteFile(filepath.Join(dir, "config.toml"), []byte("forgejo_port = 3040\nserver_port = 3041\n\n[actor_models]\nagy = \"gemini-3.8-flash\"\ncrush = \"openrouter:~deepseek/latest\"\n"), 0o600)
	inst, ok, err := LoadInstance("dev")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := SaveInstance(inst); err != nil {
		t.Fatal(err)
	}
	inst, _, _ = LoadInstance("dev")
	if inst.ActorModel("agy") != "gemini-3.8-flash" || inst.ActorModel("crush") != "openrouter:~deepseek/latest" || inst.ActorModel("claude") != "" {
		t.Fatalf("actor models %v", inst.ActorModels)
	}
}

func TestAllocatePorts(t *testing.T) {
	t.Setenv("BOTTOWN_HOME", t.TempDir())
	if f, s, _ := AllocatePorts("prod", func(int) bool { return true }); f != 3030 || s != 3031 {
		t.Fatalf("prod ports %d/%d", f, s)
	}
	if err := SaveInstance(Instance{Name: "e2e-a", ForgejoPort: 3050, ServerPort: 3051}); err != nil {
		t.Fatal(err)
	}
	if f, _, _ := AllocatePorts("e2e-b", func(int) bool { return true }); f != 3052 {
		t.Fatalf("e2e-b got %d, want 3052", f)
	}
}
