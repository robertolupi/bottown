package webui

import (
	"context"
	"errors"
	"testing"
)

func TestDiffCacheReadsOnce(t *testing.T) {
	var c diffCache
	calls := 0
	read := func(context.Context, int64) (string, error) {
		calls++
		return "diff", nil
	}
	for i := 0; i < 3; i++ {
		d, err := c.get(context.Background(), 7, read)
		if err != nil || d != "diff" {
			t.Fatalf("get: %q, %v", d, err)
		}
	}
	if calls != 1 {
		t.Errorf("read the diff %d times, want once: a merged PR does not change", calls)
	}
}

func TestDiffCacheDoesNotCacheErrors(t *testing.T) {
	var c diffCache
	calls := 0
	read := func(context.Context, int64) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("boom")
		}
		return "diff", nil
	}
	if _, err := c.get(context.Background(), 3, read); err == nil {
		t.Fatal("first get: want the read's error")
	}
	d, err := c.get(context.Background(), 3, read)
	if err != nil || d != "diff" {
		t.Fatalf("second get: %q, %v", d, err)
	}
	if calls != 2 {
		t.Errorf("read the diff %d times, want twice: a failed read is not cached", calls)
	}
}
