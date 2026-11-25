package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSweepChecksReserveAfterPrune(t *testing.T) {
	for _, tc := range []struct {
		name   string
		before diskSpace
		after  diskSpace
		low    bool
	}{
		{"full_disk_stays_full", diskSpace{0, 100}, diskSpace{0, 100}, true},
		{"partially_reclaimed", diskSpace{0, 100}, diskSpace{kibPerGiB, 100}, true},
		{"reserve_restored", diskSpace{0, 100}, diskSpace{2 * kibPerGiB, 100}, false},
		{"enough_blocks_but_low_inodes", diskSpace{2 * kibPerGiB, 0}, diskSpace{3 * kibPerGiB, 9}, true},
		{"space_decreased_during_prune", diskSpace{3 * kibPerGiB, 100}, diskSpace{2 * kibPerGiB, 100}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := 0
			read := func() (diskSpace, error) {
				checks++
				if checks == 1 {
					return tc.before, nil
				}
				return tc.after, nil
			}
			var command []string
			cfg := Config{Apply: true, MinCacheAge: 30 * time.Minute, MinFreeAfterGiB: 2, MinFreeAfterInodes: 10}
			err := sweepWith(context.Background(), cfg, read, func(_ context.Context, args ...string) error {
				command = args
				return nil
			})
			if (err != nil) != tc.low || checks != 2 || len(command) < 2 || command[1] != "prune" {
				t.Fatalf("err=%v checks=%d docker=%v", err, checks, command)
			}
		})
	}
}

func TestSpaceCheckRejectsLowOrUnreadablePaths(t *testing.T) {
	for _, tc := range []struct {
		name      string
		workspace diskSpace
		readError error
		want      int
	}{
		{"both_at_minimum", diskSpace{2 * kibPerGiB, 10}, nil, 0},
		{"low_blocks", diskSpace{2*kibPerGiB - 1, 10}, nil, 1},
		{"low_inodes", diskSpace{2 * kibPerGiB, 9}, nil, 1},
		{"unreadable", diskSpace{}, errors.New("missing path"), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var paths []string
			var out bytes.Buffer
			code := runSpaceCheck([]string{"--min-free-gib", "2", "--min-free-inodes", "10", "/docker", "/work"},
				&out, io.Discard, func(path string) (diskSpace, error) {
					paths = append(paths, path)
					if path == "/docker" {
						return diskSpace{2 * kibPerGiB, 10}, nil
					}
					return tc.workspace, tc.readError
				})
			if code != tc.want || !slices.Equal(paths, []string{"/docker", "/work"}) {
				t.Fatalf("code=%d paths=%v output=%q", code, paths, out.String())
			}
			if code == 1 && !strings.Contains(out.String(), `result=low_space path="/work"`) {
				t.Fatalf("output=%q", out.String())
			}
		})
	}
}

func TestSpaceCheckRejectsDisabledReserve(t *testing.T) {
	code := runSpaceCheck([]string{"/docker"}, io.Discard, io.Discard, func(string) (diskSpace, error) {
		t.Fatal("unexpected disk check")
		return diskSpace{}, nil
	})
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
}

func TestParseReserveFromSystemdEnvironment(t *testing.T) {
	t.Setenv("CI_CLEANER_MIN_FREE_AFTER_GIB", "15")
	t.Setenv("CI_CLEANER_MIN_FREE_AFTER_INODES", "10000")
	cfg, err := parseConfig([]string{"--docker-dir", "/docker", "--builder", "default", "--min-free-gib", "0"})
	if err != nil || cfg.MinFreeGiB != 0 || cfg.MinFreeAfterGiB != 15 || cfg.MinFreeAfterInodes != 10000 {
		t.Fatalf("config=%+v err=%v", cfg, err)
	}
	t.Setenv("CI_CLEANER_MIN_FREE_AFTER_GIB", "invalid")
	if _, err := parseConfig(nil); err == nil {
		t.Fatal("expected invalid reserve to fail")
	}
}
