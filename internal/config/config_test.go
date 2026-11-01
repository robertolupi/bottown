package config

import "testing"

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
