package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"time"
)

const resourceMinAge = 30 * time.Minute
const inspectBatchSize = 32

var ciProject = regexp.MustCompile(`^(issue156migrate[0-9]+a[0-9]+|i95cmp[0-9a-f]{32}|brc[0-9a-f]{32})$`)
var ciRepository = regexp.MustCompile(`^(issue95-dependency-test|(issue156migrate[0-9]+a[0-9]+|i95cmp[0-9a-f]{32})-[a-z0-9][a-z0-9_-]*|bot-restart-brc[0-9a-f]{32})$`)

type dockerOutput func(context.Context, ...string) ([]byte, error)
type dockerRemove func(context.Context, ...string) error

type ciImage struct {
	ID       string
	Created  time.Time
	RepoTags []string
	Config   struct{ Labels map[string]string }
}

type dockerVolume struct {
	Name      string
	CreatedAt string
	Driver    string
}

func pruneDockerResources(ctx context.Context, cfg Config) error {
	now := time.Now()
	imageErr := cleanupImages(ctx, cfg.Apply, now, dockerResult, docker)
	volumeErr := cleanupVolumes(ctx, cfg.Apply, now, dockerResult, docker)
	return errors.Join(imageErr, volumeErr)
}

func dockerResult(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("docker %q: %w", args[:2], err)
	}
	return out, nil
}

func cleanupImages(ctx context.Context, apply bool, now time.Time, output dockerOutput, remove dockerRemove) error {
	list, err := output(ctx, "image", "ls", "--quiet", "--no-trunc")
	if err != nil {
		return err
	}
	ids := strings.Fields(string(list))
	slices.Sort(ids)
	images, inspectErr := inspectResources[ciImage](ctx, output, "image", slices.Compact(ids))
	containers, err := output(ctx, "container", "ls", "--all", "--quiet", "--no-trunc")
	if err != nil {
		return errors.Join(inspectErr, err)
	}
	referenced := map[string]bool{}
	if ids := strings.Fields(string(containers)); len(ids) != 0 {
		data, err := output(ctx, append([]string{"container", "inspect", "--format", "{{.Image}}", "--"}, ids...)...)
		if err != nil {
			return errors.Join(inspectErr, err)
		}
		for _, id := range strings.Fields(string(data)) {
			referenced[id] = true
		}
	}
	eligible := 0
	for _, image := range images {
		if referenced[image.ID] || !oldCIImage(image, now) {
			continue
		}
		eligible++
		fmt.Printf("candidate=image id=%q tags=%q created=%s\n", image.ID, image.RepoTags, image.Created.Format(time.RFC3339))
		if apply {
			if err := removeCIImage(ctx, image, output, remove); err != nil {
				inspectErr = errors.Join(inspectErr, err)
			}
		}
	}
	fmt.Printf("images_eligible=%d\n", eligible)
	return inspectErr
}

func oldCIImage(image ciImage, now time.Time) bool {
	if image.ID == "" || image.Created.IsZero() || now.Sub(image.Created) < resourceMinAge {
		return false
	}
	explicit := image.Config.Labels["ci-cleaner.gc"] == "true"
	if len(image.RepoTags) == 0 {
		return explicit || ciProject.MatchString(image.Config.Labels["com.docker.compose.project"])
	}
	// An explicit image label also covers multiple tags in its own repository.
	firstRepo := imageRepository(image.RepoTags[0])
	allSameRepo, allCI := true, true
	for _, tag := range image.RepoTags {
		repo := imageRepository(tag)
		allCI = allCI && ciRepository.MatchString(repo)
		allSameRepo = allSameRepo && repo == firstRepo
	}
	return allCI || (explicit && allSameRepo)
}

func imageRepository(tag string) string {
	if i := strings.LastIndex(tag, ":"); i > strings.LastIndex(tag, "/") {
		return tag[:i]
	}
	return tag
}

func removeCIImage(ctx context.Context, image ciImage, output dockerOutput, remove dockerRemove) error {
	current, err := inspectResources[ciImage](ctx, output, "image", []string{image.ID})
	if err != nil {
		return err
	}
	slices.Sort(image.RepoTags)
	if len(current) != 1 || current[0].ID != image.ID {
		return fmt.Errorf("image %q changed during cleanup", image.ID)
	}
	slices.Sort(current[0].RepoTags)
	if !slices.Equal(image.RepoTags, current[0].RepoTags) {
		return fmt.Errorf("tags of image %q changed during cleanup", image.ID)
	}
	// Docker requires untagging aliases before the last reference can be removed by ID.
	for i := 0; i < len(image.RepoTags)-1; i++ {
		tag := image.RepoTags[i]
		id, err := output(ctx, "image", "inspect", "--format", "{{.Id}}", "--", tag)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(id)) != image.ID {
			return fmt.Errorf("image tag %q changed during cleanup", tag)
		}
		if err := remove(ctx, "image", "rm", "--no-prune", "--", tag); err != nil {
			return err
		}
	}
	return remove(ctx, "image", "rm", "--no-prune", "--", image.ID)
}

func cleanupVolumes(ctx context.Context, apply bool, now time.Time, output dockerOutput, remove dockerRemove) error {
	list, err := output(ctx, "volume", "ls", "--quiet", "--filter", "dangling=true")
	if err != nil {
		return err
	}
	names := strings.Fields(string(list))
	volumes, cleanupErr := inspectResources[dockerVolume](ctx, output, "volume", names)
	var candidates []dockerVolume
	for _, volume := range volumes {
		created, err := time.Parse(time.RFC3339Nano, volume.CreatedAt)
		if err != nil || created.IsZero() || volume.Name == "" || now.Sub(created) < resourceMinAge {
			continue
		}
		fmt.Printf("candidate=volume name=%q driver=%q created=%s\n", volume.Name, volume.Driver, volume.CreatedAt)
		candidates = append(candidates, volume)
	}
	fmt.Printf("volumes_observed=%d volumes_eligible=%d\n", len(names), len(candidates))
	if !apply {
		return cleanupErr
	}
	for len(candidates) > 0 && ctx.Err() == nil {
		batch := candidates[:min(inspectBatchSize, len(candidates))]
		candidates = candidates[len(batch):]
		expected := make(map[string]dockerVolume, len(batch))
		names := make([]string, 0, len(batch))
		for _, volume := range batch {
			names = append(names, volume.Name)
			expected[volume.Name] = volume
		}
		current, err := inspectResources[dockerVolume](ctx, output, "volume", names)
		cleanupErr = errors.Join(cleanupErr, err)
		names = names[:0]
		for _, volume := range current {
			if original, ok := expected[volume.Name]; ok && original == volume {
				names = append(names, volume.Name)
			}
		}
		if len(names) > 0 {
			// The daemon checks current container references on each non-forced removal.
			err := remove(ctx, append([]string{"volume", "rm", "--"}, names...)...)
			cleanupErr = errors.Join(cleanupErr, err)
		}
	}
	return errors.Join(cleanupErr, ctx.Err())
}

// Inspect in bounded batches. Docker can return valid records alongside an
// error when another job removes one of the listed objects during inspection.
func inspectResources[T any](ctx context.Context, output dockerOutput, kind string, ids []string) ([]T, error) {
	var records []T
	var inspectErr error
	for len(ids) > 0 && ctx.Err() == nil {
		batch := ids[:min(inspectBatchSize, len(ids))]
		ids = ids[len(batch):]
		data, err := output(ctx, append([]string{kind, "inspect", "--"}, batch...)...)
		inspectErr = errors.Join(inspectErr, err)
		var items []T
		if err := json.Unmarshal(data, &items); err != nil {
			inspectErr = errors.Join(inspectErr, fmt.Errorf("decode Docker %s inspection: %w", kind, err))
			continue
		}
		records = append(records, items...)
	}
	return records, errors.Join(inspectErr, ctx.Err())
}
