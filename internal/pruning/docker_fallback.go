package pruning

import (
	"context"

	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/docker"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

func (m *Manager) pruneContainersDocker() {
	if _, err := docker.GetInstance().PruneContainers(); err != nil {
		logging.LogError(moduleName, "Error pruning Docker containers", err)
	}
}

func (m *Manager) deleteImageDocker(nameOrID string) error {
	return docker.GetInstance().RemoveImage(nameOrID)
}

func (m *Manager) pruneAgentDocker() string {
	if _, err := docker.GetInstance().DockerPrune(); err != nil {
		logging.LogError(moduleName, "Error pruning dangling Docker images", err)
		return "\nFailure - error pruning dangling images"
	}
	logging.LogInfo(moduleName, "Pruned dangling Docker images")
	return "\nSuccess - pruned dangling images"
}

func (m *Manager) listImagesAndContainersDocker(ctx context.Context) ([]engine.ImageInfo, []engine.Container, error) {
	dockerImgs, err := docker.GetInstance().GetImages()
	if err != nil {
		return nil, nil, err
	}
	allImages := make([]engine.ImageInfo, 0, len(dockerImgs))
	for _, di := range dockerImgs {
		allImages = append(allImages, engine.ImageInfo{ID: di.ID, RepoTags: di.RepoTags})
	}

	dockerConts, err := docker.GetInstance().GetRunningContainers()
	if err != nil {
		return nil, nil, err
	}
	runningContainers := make([]engine.Container, 0, len(dockerConts))
	for _, dc := range dockerConts {
		labels := make(map[string]string, len(dc.Labels))
		for k, v := range dc.Labels {
			labels[k] = v
		}
		runningContainers = append(runningContainers, engine.Container{
			ID:     dc.ID,
			Image:  dc.Image,
			Labels: labels,
		})
	}
	return allImages, runningContainers, nil
}
