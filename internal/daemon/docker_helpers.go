package daemon

import (
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
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
