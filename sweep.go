package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

const kibPerGiB = 1024 * 1024

func sweep(ctx context.Context, cfg Config) error {
	return sweepWith(ctx, cfg, freeKiB, docker)
}

func sweepWith(ctx context.Context, cfg Config, readFree func(context.Context, string) (uint64, error), runDocker func(context.Context, ...string) error) error {
	before, err := readFree(ctx, cfg.DockerDir)
	if err != nil {
		return err
	}
	mode := "dry-run"
	if cfg.Apply {
		mode = "apply"
	}
	fmt.Printf("builder=%q mode=%s free_before_kib=%d threshold_gib=%d\n", cfg.Builder, mode, before, cfg.MinFreeGiB)

	if before/kibPerGiB >= cfg.MinFreeGiB {
		if err := runDocker(ctx, "buildx", "inspect", cfg.Builder); err != nil {
			return err
		}
		fmt.Printf("result=enough_space free_after_kib=%d\n", before)
		return nil
	}

	prune := []string{"buildx", "prune", "--builder", cfg.Builder, "--force", "--filter", "until=" + cfg.MinCacheAge.String()}
	if !cfg.Apply {
		if err := runDocker(ctx, "buildx", "du", "--builder", cfg.Builder); err != nil {
			return err
		}
		fmt.Printf("planned command: docker %q\n", prune)
		fmt.Printf("result=dry_run free_after_kib=%d\n", before)
		return nil
	}

	if err := runDocker(ctx, prune...); err != nil {
		return err
	}
	after, err := readFree(ctx, cfg.DockerDir)
	if err != nil {
		return fmt.Errorf("prune succeeded but free space could not be checked: %w", err)
	}
	fmt.Printf("result=pruned free_after_kib=%d\n", after)
	return nil
}

func freeKiB(ctx context.Context, path string) (uint64, error) {
	cmd := exec.CommandContext(ctx, "df", "-k", "--output=avail", "--", path)
	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return 0, fmt.Errorf("df %q: %w", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	available, err := strconv.ParseUint(strings.TrimSpace(lines[len(lines)-1]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("read free space for %q: %w", path, err)
	}
	return available, nil
}

func docker(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return fmt.Errorf("docker %q: %w", args, err)
	}
	return nil
}
