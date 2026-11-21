package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

const sweepTimeout = 2 * time.Minute

type Config struct {
	DockerDir string
	Builder string
	MinFreeGiB uint64
	MinCacheAge time.Duration
	Apply bool
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
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
	flags.Uint64Var(&cfg.MinFreeGiB, "min-free-gib", 25, "minimum free disk space in GiB")
	flags.DurationVar(&cfg.MinCacheAge, "min-cache-age", 30*time.Minute, "minimum time since the cache was last used")
	flags.BoolVar(&cfg.Apply, "apply", false, "perform cache cleanup")

	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			flags.SetOutput(os.Stdout)
			fmt.Fprintln(os.Stdout, "Usage: ci-cleaner [options]")
			flags.PrintDefaults()
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
	if cfg.MinFreeGiB == 0 {
		return Config{}, fmt.Errorf("--min-free-gib must be greater than zero")
	}
	if cfg.MinCacheAge <= 0 {
		return Config{}, fmt.Errorf("--min-cache-age must be greater than zero")
	}
	return cfg, nil
}
