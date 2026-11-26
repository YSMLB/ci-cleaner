package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const sweepTimeout = 2 * time.Minute

type Config struct {
	DockerDir          string
	Builder            string
	MinFreeGiB         uint64
	MinFreeAfterGiB    uint64
	MinFreeAfterInodes uint64
	MinCacheAge        time.Duration
	Apply              bool
	PruneResources     bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) > 0 && args[0] == "check-space" {
		return runSpaceCheck(args[1:], os.Stdout, os.Stderr, readDiskSpace)
	}
	cfg, err := parseConfig(args)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), sweepTimeout)
	defer cancel()

	if err := sweep(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "builder %q: %v\n", cfg.Builder, err)
		return 1
	}
	return 0
}

func parseConfig(args []string) (Config, error) {
	var cfg Config
	flags := flag.NewFlagSet("ci-cleaner", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&cfg.DockerDir, "docker-dir", "", "path on the filesystem that stores the builder cache")
	flags.StringVar(&cfg.Builder, "builder", "", "name of the CI builder")
	flags.Uint64Var(&cfg.MinFreeGiB, "min-free-gib", 25, "minimum free disk space in GiB (0 disables the threshold)")
	freeAfter, err := envUint("CI_CLEANER_MIN_FREE_AFTER_GIB")
	if err != nil {
		return Config{}, err
	}
	inodesAfter, err := envUint("CI_CLEANER_MIN_FREE_AFTER_INODES")
	if err != nil {
		return Config{}, err
	}
	flags.Uint64Var(&cfg.MinFreeAfterGiB, "min-free-after-gib", freeAfter, "required free disk space after cleanup in GiB")
	flags.Uint64Var(&cfg.MinFreeAfterInodes, "min-free-after-inodes", inodesAfter, "required free inodes after cleanup")
	flags.DurationVar(&cfg.MinCacheAge, "min-cache-age", 30*time.Minute, "minimum time since the cache was last used")
	flags.BoolVar(&cfg.Apply, "apply", false, "perform cache cleanup")
	pruneResources := false
	if raw := os.Getenv("CI_CLEANER_PRUNE_RESOURCES"); raw != "" {
		pruneResources, err = strconv.ParseBool(raw)
		if err != nil {
			return Config{}, fmt.Errorf("CI_CLEANER_PRUNE_RESOURCES must be a boolean: %w", err)
		}
	}
	flags.BoolVar(&cfg.PruneResources, "prune-ci-resources", pruneResources, "include old CI images and detached local volumes")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(os.Stdout)
			fmt.Fprintln(os.Stdout, "Usage: ci-cleaner [options]")
			flags.PrintDefaults()
			fmt.Fprintln(os.Stdout, "       ci-cleaner check-space --help")
		}
		return Config{}, err
	}
	if flags.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", flags.Arg(0))
	}
	if strings.TrimSpace(cfg.DockerDir) == "" {
		return Config{}, fmt.Errorf("--docker-dir is required")
	}
	if strings.TrimSpace(cfg.Builder) == "" {
		return Config{}, fmt.Errorf("--builder is required")
	}
	if cfg.MinCacheAge <= 0 {
		return Config{}, fmt.Errorf("--min-cache-age must be greater than zero")
	}
	return cfg, nil
}

func envUint(name string) (uint64, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be a non-negative integer: %w", name, err)
	}
	return value, nil
}
