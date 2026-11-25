//go:build !linux

package main

import (
	"context"
	"errors"
)

func sweep(context.Context, Config) error {
	return errors.New("ci-cleaner requires Linux")
}

func readDiskSpace(string) (diskSpace, error) {
	return diskSpace{}, errors.New("ci-cleaner check-space requires Linux")
}
