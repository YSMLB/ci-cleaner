package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"syscall"
)

func sweep(ctx context.Context, cfg Config) error {
	dir, err := os.Open(cfg.DockerDir)
	if err != nil {
		return err
	}
	defer dir.Close()
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%q must be a directory", cfg.DockerDir)
	}
	fd := int(dir.Fd())
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			fmt.Printf("builder=%q result=already_running\n", cfg.Builder)
			return nil
		}
		return fmt.Errorf("lock %q: %w", cfg.DockerDir, err)
	}
	readFree := func(context.Context, string) (uint64, error) {
		var stat syscall.Statfs_t
		if err := syscall.Fstatfs(fd, &stat); err != nil {
			return 0, fmt.Errorf("check disk space: %w", err)
		}
		return uint64(stat.Bavail) * uint64(stat.Bsize) / 1024, nil
	}
	return sweepWith(ctx, cfg, readFree, docker)
}
