package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

const kibPerGiB = 1024 * 1024

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

	if cfg.MinFreeGiB != 0 && before/kibPerGiB >= cfg.MinFreeGiB {
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

	status := "pruned"
	if cfg.MinFreeGiB != 0 && after/kibPerGiB < cfg.MinFreeGiB{
		status = "below_threshold_after_prune"
	}

	delta := fmt.Sprintf("%d", after-before)
	if after < before{
		delta = fmt.Sprintf("-%d", before-after)
	}

	fmt.Printf("result=%s free_before_kib=%d, free_after_kib=%d free_delta_kib=%s thresjold_gib=%d\n",
	status, before, after, delta, cfg.MinFreeGiB,
)

	return nil
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
