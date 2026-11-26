package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestCleanupImagesKeepsForeignYoungAndInUseImages(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)
	images := []ciImage{
		{ID: "ci", Created: old, RepoTags: []string{"issue156migrate123a1-app:latest"}},
		{ID: "in-use", Created: old, RepoTags: []string{"issue95-dependency-test:older"}},
		{ID: "young", Created: now, RepoTags: []string{"issue95-dependency-test:new"}},
		{ID: "foreign", Created: old, RepoTags: []string{"postgres:16"}},
		{ID: "shared", Created: old, RepoTags: []string{"issue95-dependency-test:old", "keep:latest"}},
	}
	output := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] + " " + args[1] {
		case "image ls":
			return []byte("ci in-use young foreign shared"), nil
		case "image inspect":
			return json.Marshal(images)
		case "container ls":
			return []byte("stopped-container"), nil
		case "container inspect":
			if args[len(args)-1] == "stopped-container" {
				return []byte("in-use"), nil
			}
		}
		t.Fatalf("unexpected observation command %v", args)
		return nil, nil
	}
	var removed [][]string
	remove := func(_ context.Context, args ...string) error {
		removed = append(removed, args)
		return nil
	}
	if err := cleanupImages(context.Background(), false, now, output, remove); err != nil || len(removed) != 0 {
		t.Fatalf("dry-run error=%v removed=%v", err, removed)
	}
	if err := cleanupImages(context.Background(), true, now, output, remove); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"image", "rm", "--no-prune", "--", "ci"}}
	if !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed=%v want=%v", removed, want)
	}
}

func TestCleanupVolumesRequiresTwoObservationsAndChecksIdentity(t *testing.T) {
	now := time.Now()
	old := now.Add(-time.Hour)
	stable := dockerVolume{Name: "stable", Driver: "local", CreatedAt: old}
	replaced := dockerVolume{Name: "replaced", Driver: "local", CreatedAt: old}
	state := filepath.Join(t.TempDir(), "state.json")
	output := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[1] {
		case "ls":
			return []byte("stable replaced"), nil
		case "inspect":
			switch args[len(args)-1] {
			case "stable":
				return json.Marshal([]dockerVolume{stable})
			case "replaced":
				if len(args) == 5 {
					return json.Marshal([]dockerVolume{stable, replaced})
				}
				replacement := replaced
				replacement.CreatedAt = now
				return json.Marshal([]dockerVolume{replacement})
			}
		}
		t.Fatalf("unexpected observation command %v", args)
		return nil, nil
	}
	var removed [][]string
	remove := func(_ context.Context, args ...string) error {
		removed = append(removed, args)
		return nil
	}
	check := func(apply bool, at time.Time, wantCount int) {
		t.Helper()
		if err := cleanupVolumes(context.Background(), apply, at, state, output, remove); err != nil || len(removed) != wantCount {
			t.Fatalf("error=%v removed=%v", err, removed)
		}
	}
	check(false, now, 0)
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("dry-run must not write state: %v", err)
	}
	check(true, now, 0)
	check(true, now.Add(29*time.Minute), 0)
	check(false, now.Add(30*time.Minute), 0)
	check(true, now.Add(30*time.Minute), 1)
	if want := [][]string{{"volume", "rm", "--", "stable"}}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed=%v want=%v", removed, want)
	}
}
