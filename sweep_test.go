package main

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestSweep(t *testing.T) {

	type test struct {
		name     string
		kib      uint64
		minFree  uint64
		apply    bool
		wantArgs []string
	}

	testCase := []test{
		{
			name:     "dry_run_below_threshold",
			kib:      24 * 1024 * 1024,
			minFree:  25,
			apply:    false,
			wantArgs: []string{"buildx", "du", "--builder", "ci-builder"},
		},
		{
			name:     "exactly_at_threshold",
			kib:      25 * 1024 * 1024,
			minFree:  25,
			apply:    false,
			wantArgs: []string{"buildx", "inspect", "ci-builder"},
		},
		{
			name:     "apply_below_threshold",
			kib:      24 * 1024 * 1024,
			minFree:  25,
			apply:    true,
			wantArgs: []string{"buildx", "prune", "--builder", "ci-builder", "--force", "--filter", "until=30m0s"},
		},
		{
			name:     "apply_without_threshold",
			kib:      100 * 1024 * 1024,
			wantArgs: []string{"buildx", "prune", "--builder", "ci-builder", "--force", "--filter", "until=30m0s"},
			apply:    true,
		},
	}

	for _, tc := range testCase {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{
				DockerDir:   "/var/lib/docker",
				Builder:     "ci-builder",
				MinFreeGiB:  tc.minFree,
				MinCacheAge: 30 * time.Minute,
				Apply:       tc.apply,
			}

			var calls [][]string

			readFree := func() (diskSpace, error) {
				return diskSpace{freeKiB: tc.kib}, nil
			}

			runDocker := func(_ context.Context, args ...string) error {
				calls = append(calls, args)
				return nil
			}

			err := sweepWith(context.Background(), cfg, readFree, runDocker, nil)
			if err != nil {
				t.Fatalf("sweepWith: %v", err)
			}
			if len(calls) != 1 || !slices.Equal(calls[0], tc.wantArgs) {
				t.Fatalf("Docker calls = %q, want one call with %q", calls, tc.wantArgs)
			}
		})
	}
}
