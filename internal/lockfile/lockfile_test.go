package lockfile

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestAcquireHonorsCancellationAndRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml.lock")
	first, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Acquire(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("second acquire = %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatal(err)
	}

	second, err := Acquire(context.Background(), path)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatal(err)
	}
}
