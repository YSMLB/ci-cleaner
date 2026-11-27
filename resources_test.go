package main

import (
	"context"
	"encoding/json"
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
		{ID: "aliases", Created: old, RepoTags: []string{"issue95-dependency-test:a", "issue95-dependency-test:b"}},
	}
	output := func(_ context.Context, args ...string) ([]byte, error) {
		switch args[0] + " " + args[1] {
		case "image ls":
			return []byte("ci in-use young foreign shared"), nil
		case "image inspect":
			if args[2] == "--format" {
				return []byte("aliases"), nil
			}
			if len(args) == 4 {
				for _, image := range images {
					if image.ID == args[3] {
						return json.Marshal([]ciImage{image})
					}
				}
			}
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
	want := [][]string{{"image", "rm", "--no-prune", "--", "ci"}, {"image", "rm", "--no-prune", "--", "issue95-dependency-test:a"}, {"image", "rm", "--no-prune", "--", "aliases"}}
	if !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed=%v want=%v", removed, want)
	}
}

func TestFirstPassVolumes(t *testing.T) {
	now := time.Now().UTC()
	old := now.Add(-30 * time.Minute).Format(time.RFC3339Nano)
	volumes := []dockerVolume{
		{Name: "named", CreatedAt: old, Driver: "local"},
		{Name: "new", CreatedAt: now.Add(-29 * time.Minute).Format(time.RFC3339Nano)},
		{Name: "recreated", CreatedAt: old, Driver: "local"},
		{Name: "unknown", CreatedAt: ""},
	}
	output := func(_ context.Context, args ...string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"volume", "ls", "--quiet", "--filter", "dangling=true"}) {
			return []byte("named new recreated unknown"), nil
		}
		if reflect.DeepEqual(args, []string{"volume", "inspect", "--", "named", "recreated"}) {
			replacement := volumes[2]
			replacement.CreatedAt = now.Format(time.RFC3339Nano)
			return json.Marshal([]dockerVolume{volumes[0], replacement})
		}
		if reflect.DeepEqual(args, []string{"volume", "inspect", "--", "named", "new", "recreated", "unknown"}) {
			return json.Marshal(volumes)
		}
		t.Fatalf("unexpected observation command %v", args)
		return nil, nil
	}
	var removed [][]string
	remove := func(_ context.Context, args ...string) error {
		removed = append(removed, args)
		return nil
	}
	if err := cleanupVolumes(context.Background(), false, now, output, remove); err != nil || len(removed) != 0 {
		t.Fatalf("dry-run error=%v removed=%v", err, removed)
	}
	if err := cleanupVolumes(context.Background(), true, now, output, remove); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{"volume", "rm", "--", "named"}}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed=%v want=%v", removed, want)
	}
}

func TestImageProvenance(t *testing.T) {
	const runID = "0123456789abcdef0123456789abcdef"
	now := time.Now()
	for _, tc := range []struct {
		tag, project string
		want         bool
	}{
		{"i95cmp" + runID + "-webhook:latest", "", true},
		{"bot-restart-brc" + runID + ":test", "", true},
		{"", "i95cmp" + runID, true},
		{"", "", false},
		{"foreign/i95cmp" + runID + "-webhook:latest", "", false},
		{"i95cmp-test-app:latest", "", false},
		{"keep:latest", "issue156migrate123a1", false},
	} {
		image := ciImage{Created: now.Add(-resourceMinAge), ID: "test"}
		if tc.tag != "" {
			image.RepoTags = []string{tc.tag}
		}
		image.Config.Labels = map[string]string{"com.docker.compose.project": tc.project}
		if got := oldCIImage(image, now); got != tc.want {
			t.Errorf("tag=%q project=%q result=%v", tc.tag, tc.project, got)
		}
	}
}
