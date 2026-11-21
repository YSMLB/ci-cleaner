package main

import (
	"context"
	"os"
	"syscall"
	"testing"
)

func TestSweepSkipsLockedDirectory(t *testing.T) {
	path := t.TempDir()
	dir, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	if err := syscall.Flock(int(dir.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", "")
	if err := sweep(context.Background(), Config{DockerDir: path, Builder: "test"}); err != nil {
		t.Fatalf("concurrent sweep: %v", err)
	}
}
