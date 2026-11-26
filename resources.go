package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const resourceMinAge = 30 * time.Minute
const volumeStatePath = "/var/lib/ci-cleaner/unattached-volumes.json"

var migrationImage = regexp.MustCompile(`^issue156migrate[0-9]+a[0-9]+-(app|migrate|webhook|bot|max_bot|celery_worker|celery_beat)$`)

type dockerOutput func(context.Context, ...string) ([]byte, error)

type ciImage struct {
	ID       string
	Created  time.Time
	RepoTags []string
	Config   struct{ Labels map[string]string }
}

type dockerVolume struct {
	Name      string
	CreatedAt time.Time
	Driver    string
	Options   map[string]string
}

type volumeObservation struct {
	CreatedAt time.Time
	FirstSeen time.Time
}

func pruneDockerResources(ctx context.Context, cfg Config) error {
	return cleanupResources(ctx, cfg.Apply, time.Now(), volumeStatePath, dockerResult, docker)
}

func cleanupResources(ctx context.Context, apply bool, now time.Time, statePath string, output dockerOutput, remove func(context.Context, ...string) error) error {
	imageErr := cleanupImages(ctx, apply, now, output, remove)
	volumeErr := cleanupVolumes(ctx, apply, now, statePath, output, remove)
	return errors.Join(imageErr, volumeErr)
}

func dockerResult(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("docker %q: %w", args[:2], err)
	}
	return out, nil
}

func cleanupImages(ctx context.Context, apply bool, now time.Time, output dockerOutput, remove func(context.Context, ...string) error) error {
	list, err := output(ctx, "image", "ls", "--quiet", "--no-trunc")
	if err != nil {
		return err
	}
	ids := strings.Fields(string(list))
	if len(ids) == 0 {
		return nil
	}
	slices.Sort(ids)
	var images []ciImage
	if err := inspectResources(ctx, output, "image", slices.Compact(ids), &images); err != nil {
		return err
	}
	containers, err := output(ctx, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return err
	}
	referenced := map[string]bool{}
	if ids := strings.Fields(string(containers)); len(ids) != 0 {
		data, err := output(ctx, append([]string{"container", "inspect", "--format", "{{.Image}}", "--"}, ids...)...)
		if err != nil {
			return err
		}
		for _, id := range strings.Fields(string(data)) {
			referenced[id] = true
		}
	}
	var cleanupErr error
	for _, image := range images {
		if referenced[image.ID] || !oldCIImage(image, now) {
			continue
		}
		fmt.Printf("candidate=image id=%q tags=%q\n", image.ID, image.RepoTags)
		if apply {
			if err := remove(ctx, "image", "rm", "--no-prune", "--", image.ID); err != nil {
				fmt.Fprintln(os.Stderr, err)
				cleanupErr = errors.New("some CI images could not be removed")
			}
		}
	}
	return cleanupErr
}

func oldCIImage(image ciImage, now time.Time) bool {
	if image.ID == "" || image.Created.IsZero() || now.Sub(image.Created) < resourceMinAge {
		return false
	}
	// Multiple tags can include a retained reference to the same image.
	if len(image.RepoTags) > 1 {
		return false
	}
	if image.Config.Labels["ci-cleaner.gc"] == "true" {
		return true
	}
	if len(image.RepoTags) != 1 {
		return false
	}
	repository, _, _ := strings.Cut(image.RepoTags[0], ":")
	return repository == "issue95-dependency-test" || migrationImage.MatchString(repository)
}

func cleanupVolumes(ctx context.Context, apply bool, now time.Time, statePath string, output dockerOutput, remove func(context.Context, ...string) error) error {
	previous := map[string]volumeObservation{}
	data, err := os.ReadFile(statePath)
	if err == nil {
		if err := json.Unmarshal(data, &previous); err != nil {
			return fmt.Errorf("decode volume observations: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	list, err := output(ctx, "volume", "ls", "--quiet", "--filter", "dangling=true", "--filter", "driver=local")
	if err != nil {
		return err
	}
	var volumes []dockerVolume
	if names := strings.Fields(string(list)); len(names) != 0 {
		if err := inspectResources(ctx, output, "volume", names, &volumes); err != nil {
			return err
		}
	}
	next, candidates := observeVolumes(previous, volumes, now)
	if apply {
		if err := saveVolumeObservations(statePath, next); err != nil {
			return err
		}
	}
	var cleanupErr error
	for _, volume := range candidates {
		fmt.Printf("candidate=volume name=%q unbound_observed_since=%s\n", volume.Name, next[volume.Name].FirstSeen.Format(time.RFC3339))
		if !apply {
			continue
		}
		var current []dockerVolume
		err := inspectResources(ctx, output, "volume", []string{volume.Name}, &current)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			cleanupErr = errors.New("some detached volumes could not be checked")
			continue
		}
		if len(current) != 1 || current[0].Name != volume.Name || current[0].Driver != "local" || len(current[0].Options) != 0 ||
			!current[0].CreatedAt.Equal(volume.CreatedAt) {
			continue
		}
		// Docker refuses to remove a volume referenced by any container.
		if err := remove(ctx, "volume", "rm", "--", volume.Name); err != nil {
			fmt.Fprintln(os.Stderr, err)
			cleanupErr = errors.New("some detached volumes could not be removed")
		}
	}
	fmt.Printf("volumes_observed=%d volumes_eligible=%d\n", len(next), len(candidates))
	return cleanupErr
}

func observeVolumes(previous map[string]volumeObservation, volumes []dockerVolume, now time.Time) (map[string]volumeObservation, []dockerVolume) {
	next := make(map[string]volumeObservation, len(volumes))
	var candidates []dockerVolume
	for _, volume := range volumes {
		if volume.Name == "" || volume.Driver != "local" || len(volume.Options) != 0 || volume.CreatedAt.IsZero() {
			continue
		}
		observation := previous[volume.Name]
		if !observation.CreatedAt.Equal(volume.CreatedAt) || observation.FirstSeen.IsZero() || observation.FirstSeen.After(now) {
			observation = volumeObservation{CreatedAt: volume.CreatedAt, FirstSeen: now}
		}
		next[volume.Name] = observation
		if now.Sub(observation.FirstSeen) >= resourceMinAge && now.Sub(volume.CreatedAt) >= resourceMinAge {
			candidates = append(candidates, volume)
		}
	}
	return next, candidates
}

func inspectResources[T any](ctx context.Context, output dockerOutput, kind string, ids []string, dst *[]T) error {
	data, err := output(ctx, append([]string{kind, "inspect", "--"}, ids...)...)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("decode Docker %s inspection: %w", kind, err)
	}
	return nil
}

func saveVolumeObservations(path string, observations map[string]volumeObservation) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, "unattached-volumes-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	err = json.NewEncoder(file).Encode(observations)
	if err := errors.Join(err, file.Close()); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
