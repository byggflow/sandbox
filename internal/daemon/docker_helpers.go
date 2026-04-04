package daemon

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
)

// containerCommitOptions builds commit options that tag the image.
func containerCommitOptions(imageTag string) container.CommitOptions {
	// imageTag is "byggflow-sandbox:tpl-xxxx"
	parts := strings.SplitN(imageTag, ":", 2)
	ref := parts[0]
	tag := ""
	if len(parts) == 2 {
		tag = parts[1]
	}
	return container.CommitOptions{
		Reference: ref + ":" + tag,
	}
}

// imageRemoveOptions returns default options for removing a Docker image.
func imageRemoveOptions() image.RemoveOptions {
	return image.RemoveOptions{
		Force:         true,
		PruneChildren: true,
	}
}

// DockerTemplateBackend implements TemplateBackend using Docker commit.
type DockerTemplateBackend struct {
	Docker *client.Client
}

// Capture commits a running container as a Docker image.
func (b *DockerTemplateBackend) Capture(ctx context.Context, containerID, tag string) (string, int64, error) {
	commitResp, err := b.Docker.ContainerCommit(ctx, containerID, containerCommitOptions(tag))
	if err != nil {
		return "", 0, fmt.Errorf("docker commit: %w", err)
	}

	var size int64
	inspect, _, err := b.Docker.ImageInspectWithRaw(ctx, commitResp.ID)
	if err == nil {
		size = inspect.Size
	}

	return tag, size, nil
}

// Remove deletes a Docker image.
func (b *DockerTemplateBackend) Remove(ctx context.Context, ref string) error {
	_, err := b.Docker.ImageRemove(ctx, ref, imageRemoveOptions())
	return err
}
