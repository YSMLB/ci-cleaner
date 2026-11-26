package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func sweepWith(ctx context.Context, cfg Config, readFree func() (diskSpace, error), runDocker func(context.Context, ...string) error, pruneResources func(context.Context, Config) error) error {
	before, err := readFree()
	if err != nil {
		return err
	}
	mode := "dry-run"
	if cfg.Apply {
		mode = "apply"
	}
	fmt.Printf("builder=%q mode=%s free_before_kib=%d threshold_gib=%d\n", cfg.Builder, mode, before.freeKiB, cfg.MinFreeGiB)

	if cfg.MinFreeGiB != 0 && !before.below(cfg.MinFreeGiB, 0) &&
		!before.below(cfg.MinFreeAfterGiB, cfg.MinFreeAfterInodes) {
		if err := runDocker(ctx, "buildx", "inspect", cfg.Builder); err != nil {
			return err
		}
		fmt.Printf("result=enough_space free_after_kib=%d\n", before.freeKiB)
		return nil
	}

	prune := []string{"buildx", "prune", "--builder", cfg.Builder, "--force", "--filter", "until=" + cfg.MinCacheAge.String()}
	if !cfg.Apply {
		scanErr := runDocker(ctx, "buildx", "du", "--builder", cfg.Builder)
		if cfg.PruneResources {
			scanErr = errors.Join(scanErr, pruneResources(ctx, cfg))
		}
		if scanErr != nil {
			return scanErr
		}
		fmt.Printf("planned command: docker %q\n", prune)
		fmt.Printf("result=dry_run free_after_kib=%d\n", before.freeKiB)
		return nil
	}

	pruneErr := runDocker(ctx, prune...)
	if cfg.PruneResources {
		pruneErr = errors.Join(pruneErr, pruneResources(ctx, cfg))
	}
	after, err := readFree()
	if err != nil {
		return errors.Join(pruneErr, fmt.Errorf("check free space after prune: %w", err))
	}

	status := "pruned"
	lowReserve := after.below(cfg.MinFreeAfterGiB, cfg.MinFreeAfterInodes)
	if lowReserve {
		status = "low_space_after_prune"
	} else if cfg.MinFreeGiB != 0 && after.below(cfg.MinFreeGiB, 0) {
		status = "below_threshold_after_prune"
	}
	if pruneErr != nil && !lowReserve {
		status = "prune_error"
	}

	var delta string
	if after.freeKiB < before.freeKiB {
		delta = fmt.Sprintf("-%d", before.freeKiB-after.freeKiB)
	} else {
		delta = fmt.Sprintf("%d", after.freeKiB-before.freeKiB)
	}

	fmt.Printf("result=%s free_before_kib=%d free_after_kib=%d free_delta_kib=%s threshold_gib=%d free_after_inodes=%d min_free_after_gib=%d min_free_after_inodes=%d\n",
		status, before.freeKiB, after.freeKiB, delta, cfg.MinFreeGiB, after.freeInodes, cfg.MinFreeAfterGiB, cfg.MinFreeAfterInodes)
	if lowReserve {
		return errors.Join(pruneErr, fmt.Errorf("disk reserve not restored at %q after prune", cfg.DockerDir))
	}
	return pruneErr
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
