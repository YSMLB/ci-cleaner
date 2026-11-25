package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

const kibPerGiB = 1024 * 1024

type diskSpace struct {
	freeKiB    uint64
	freeInodes uint64
}

func (s diskSpace) below(minGiB, minInodes uint64) bool {
	return s.freeKiB/kibPerGiB < minGiB || s.freeInodes < minInodes
}

func runSpaceCheck(args []string, out, stderr io.Writer, read func(string) (diskSpace, error)) int {
	flags := flag.NewFlagSet("check-space", flag.ContinueOnError)
	flags.SetOutput(stderr)
	minGiB := flags.Uint64("min-free-gib", 0, "minimum free GiB on each path's filesystem")
	minInodes := flags.Uint64("min-free-inodes", 0, "minimum free inodes on each path's filesystem")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() == 0 || (*minGiB == 0 && *minInodes == 0) {
		fmt.Fprintln(stderr, "usage: ci-cleaner check-space --min-free-gib N [--min-free-inodes N] PATH [PATH...]")
		return 2
	}

	exitCode := 0
	for _, path := range flags.Args() {
		space, err := read(path)
		if err != nil {
			fmt.Fprintf(stderr, "check disk space at %q: %v\n", path, err)
			return 2
		}
		result := "enough_space"
		if space.below(*minGiB, *minInodes) {
			result = "low_space"
			exitCode = 1
		}
		fmt.Fprintf(out, "result=%s path=%q free_kib=%d free_inodes=%d min_free_gib=%d min_free_inodes=%d\n",
			result, path, space.freeKiB, space.freeInodes, *minGiB, *minInodes)
	}
	return exitCode
}
