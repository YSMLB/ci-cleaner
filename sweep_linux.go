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
	readFree := func() (diskSpace, error) {
		var stat syscall.Statfs_t
		if err := syscall.Fstatfs(fd, &stat); err != nil {
			return diskSpace{}, fmt.Errorf("check disk space: %w", err)
		}
		return fromStatfs(stat), nil
	}
	return sweepWith(ctx, cfg, readFree, docker, pruneDockerResources)
}

func readDiskSpace(path string) (diskSpace, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return diskSpace{}, err
	}
	return fromStatfs(stat), nil
}

func fromStatfs(stat syscall.Statfs_t) diskSpace {
	return diskSpace{
		freeKiB:    uint64(stat.Bavail) * uint64(stat.Bsize) / 1024,
		freeInodes: stat.Ffree,
	}
}
