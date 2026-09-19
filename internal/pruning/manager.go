package pruning

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
)

const (
	moduleName = "Edgelet Pruning Manager"
)

// Manager manages image pruning for all container engine types.
type Manager struct {
	config          *config.Config
	containerEngine engine.ContainerEngine // set by supervisor for all active engines
	// getMicroserviceImages returns the image names of ALL currently configured
	// microservices (running + stopped/restarting). Set by the supervisor via
	// SetGetMicroservicesCallback.
	getMicroserviceImages func() []string
	isPruning             bool
	mu                    sync.Mutex
	ctx                   context.Context
	cancel                context.CancelFunc
	thresholdTicker       *time.Ticker
	frequencyTicker       *time.Ticker
	// freqCtx/freqCancel govern only the frequency worker goroutine so it can
	// be canceled independently when ChangePruningFreqInterval is called
	// without disrupting the threshold worker (which uses the main ctx).
	freqCtx    context.Context
	freqCancel context.CancelFunc

	// Optional test hooks to observe scheduled prune ordering without touching
	// real runtime daemons. pruneVolumesHook is retained so tests can prove
	// scheduled prune never reclaims persistent VOLUME data.
	pruneContainersHook func()
	pruneVolumesHook    func()
	pruneImagesHook     func()
	pruneModelsHook     func()
}

var (
	instance *Manager
	once     sync.Once
)

// GetInstance returns the singleton pruning Manager instance.
func GetInstance() *Manager {
	once.Do(func() {
		instance = &Manager{
			config: config.GetInstance(),
		}
		instance.ctx, instance.cancel = context.WithCancel(context.Background())
		instance.freqCtx, instance.freqCancel = context.WithCancel(context.Background())
	})
	return instance
}

// SetGetMicroservicesCallback sets a callback that returns image names for ALL
// currently configured microservices (controller-managed and local-deployed,
// running or not). These images are protected from scheduled/threshold pruning.
// Called by the supervisor after ProcessManager is wired up.
func (m *Manager) SetGetMicroservicesCallback(fn func() []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getMicroserviceImages = fn
}

// SetEngine sets the ContainerEngine to use for pruning.
// When set, pruning delegates to engine.PruneImages() instead of the Docker-specific logic,
// enabling correct pruning for iofog/containerd and other non-Docker engines.
func (m *Manager) SetEngine(e engine.ContainerEngine) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.containerEngine = e
}

// SetPruneModelsCallback runs on the same scheduled tick as image prune.
func (m *Manager) SetPruneModelsCallback(fn func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneModelsHook = fn
}

// Start starts the Edgelet Pruning Manager
func (m *Manager) Start() error {
	logging.LogInfo(moduleName, "Starting Edgelet Pruning Manager")

	// Reset contexts on each start to support supervisor restart cycles.
	if m.cancel != nil {
		m.cancel()
	}
	if m.freqCancel != nil {
		m.freqCancel()
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.freqCtx, m.freqCancel = context.WithCancel(context.Background())

	// Start threshold-based pruning (check every 30 minutes)
	m.thresholdTicker = time.NewTicker(30 * time.Minute)
	go m.thresholdPruningWorker()

	// Start frequency-based pruning if configured (0 = disabled).
	// The first run waits for the ticker; enabling frequency does not prune
	// immediately. Disk-threshold prune and explicit system prune stay on-demand.
	pruningFrequency := m.config.PruningFrequency
	if pruningFrequency > 0 {
		duration := time.Duration(pruningFrequency) * time.Hour
		m.frequencyTicker = time.NewTicker(duration)
		go m.frequencyPruningWorker()
		logging.LogInfo(moduleName, fmt.Sprintf("Edgelet Pruning Manager started with frequency: %d hours", pruningFrequency))
	} else {
		logging.LogInfo(moduleName, "Edgelet Pruning Manager started without frequency-based pruning (frequency set to 0)")
	}

	return nil
}

// Stop stops the Edgelet Pruning Manager
func (m *Manager) Stop() error {
	logging.LogInfo(moduleName, "Stopping Edgelet Pruning Manager")
	if m.cancel != nil {
		m.cancel()
	}
	if m.thresholdTicker != nil {
		m.thresholdTicker.Stop()
	}
	if m.frequencyTicker != nil {
		m.frequencyTicker.Stop()
	}
	return nil
}

// thresholdPruningWorker checks disk threshold and prunes if needed
func (m *Manager) thresholdPruningWorker() {
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-m.thresholdTicker.C:
			m.triggerPruneOnThresholdBreach()
		}
	}
}

// frequencyPruningWorker prunes on frequency interval.
// It uses freqCtx (not the main ctx) so it can be canceled independently
// by ChangePruningFreqInterval without stopping the threshold worker.
func (m *Manager) frequencyPruningWorker() {
	for {
		select {
		case <-m.freqCtx.Done():
			return
		case <-m.frequencyTicker.C:
			m.triggerPruneOnFrequency()
		}
	}
}

// triggerPruneOnThresholdBreach triggers prune when available disk is below threshold
func (m *Manager) triggerPruneOnThresholdBreach() {
	m.mu.Lock()
	if m.isPruning {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	rcmStatus := statusreporter.GetInstance().GetResourceConsumptionManagerStatus()
	if rcmStatus == nil {
		return
	}

	// AvailableDiskThreshold is a percentage (0-100)
	// Prune when the available disk percentage falls below the configured threshold.
	// Guard against division by zero when TotalDiskSpace is not yet populated.
	if rcmStatus.TotalDiskSpace <= 0 {
		return
	}
	availablePercent := rcmStatus.AvailableDisk * 100 / rcmStatus.TotalDiskSpace
	if availablePercent < m.config.AvailableDiskThreshold {
		m.mu.Lock()
		m.isPruning = true
		m.mu.Unlock()
		defer func() {
			m.mu.Lock()
			m.isPruning = false
			m.mu.Unlock()
		}()

		logging.LogInfo(moduleName, "Disk threshold breached, pruning runtime artifacts")
		m.runScheduledPrune()
	}
}

// triggerPruneOnFrequency triggers prune on frequency interval
func (m *Manager) triggerPruneOnFrequency() {
	m.mu.Lock()
	if m.isPruning {
		m.mu.Unlock()
		return
	}
	m.isPruning = true
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		m.isPruning = false
		m.mu.Unlock()
	}()

	logging.LogInfo(moduleName, "Start pruning job")
	m.runScheduledPrune()
	logging.LogInfo(moduleName, "Pruning of unwanted images finished")
}

// runScheduledPrune reclaims unused images, unused local models, and
// unmanaged stopped containers. Persistent VOLUME data under volumes/data
// and volumes/shared is not deleted here; operators reclaim it explicitly.
func (m *Manager) runScheduledPrune() {
	m.pruneContainers()
	m.pruneImagesRunner()
	m.pruneModels()
}

func (m *Manager) pruneContainers() {
	if m.pruneContainersHook != nil {
		m.pruneContainersHook()
		return
	}
	ctx := context.Background()
	m.mu.Lock()
	eng := m.containerEngine
	m.mu.Unlock()
	if eng != nil {
		if _, err := eng.PruneContainers(ctx); err != nil {
			logging.LogError(moduleName, "Error pruning containers via container engine", err)
		}
		return
	}
	m.pruneContainersDocker()
}

func (m *Manager) pruneImagesRunner() {
	if m.pruneImagesHook != nil {
		m.pruneImagesHook()
		return
	}
	m.pruneImages()
}

func (m *Manager) pruneModels() {
	if m.pruneModelsHook != nil {
		m.pruneModelsHook()
	}
}

// pruneImages removes unused images using the unified getUnwantedImagesList() logic
// for all engine types. Scheduled/threshold pruning keeps images for configured
// microservices (controller and local) and images still referenced by running
// containers, then deletes everything else.
func (m *Manager) pruneImages() {
	ctx := context.Background()
	m.mu.Lock()
	eng := m.containerEngine
	m.mu.Unlock()

	unwanted := m.getUnwantedImagesList(ctx, eng)
	if len(unwanted) == 0 {
		return
	}
	for _, nameOrID := range unwanted {
		logging.LogInfo(moduleName, fmt.Sprintf("Removing unwanted image: %s", nameOrID))
		var err error
		if eng != nil {
			err = eng.DeleteImage(ctx, nameOrID)
		} else {
			err = m.deleteImageDocker(nameOrID)
		}
		if err != nil {
			logging.LogError(moduleName, fmt.Sprintf("Error removing image %s", nameOrID), err)
		}
	}
}

// getUnwantedImagesList returns image IDs/names that should be deleted during
// scheduled or threshold pruning. The logic:
//
//   - Protect images used by any running container (managed local/controller
//     workloads and unmanaged containers).
//   - Protect images with a known in-use count greater than zero (covers CRI
//     pause/sandbox images skipped by GetRunningContainers).
//   - Protect images for ALL configured microservices via getMicroserviceImages
//     (controller-managed and local-deployed, including stopped/restarting).
//   - Delete every other image.
//
// Works for all engine types via the ContainerEngine interface. When eng is nil
// it falls back to the Docker client.
func (m *Manager) getUnwantedImagesList(ctx context.Context, eng engine.ContainerEngine) []string {
	var allImages []engine.ImageInfo
	var runningContainers []engine.Container

	if eng != nil {
		imgs, err := eng.ListImages(ctx)
		if err != nil {
			logging.LogError(moduleName, "Failed to list images via engine", err)
			return []string{}
		}
		allImages = imgs

		conts, err := eng.GetRunningContainers()
		if err != nil {
			logging.LogError(moduleName, "Failed to get running containers via engine", err)
			return []string{}
		}
		runningContainers = conts
	} else {
		var err error
		allImages, runningContainers, err = m.listImagesAndContainersDocker(ctx)
		if err != nil {
			logging.LogError(moduleName, "Failed to list images via Docker fallback", err)
			return []string{}
		}
	}

	logging.LogDebug(moduleName, fmt.Sprintf("Total images: %d", len(allImages)))

	// Build a lookup map from tag → image ID for quick resolution.
	tagToID := make(map[string]string, len(allImages))
	for _, img := range allImages {
		tagToID[img.ID] = img.ID
		for _, tag := range img.RepoTags {
			tagToID[tag] = img.ID
		}
	}

	usedImageIDs := make(map[string]bool)

	for _, cont := range runningContainers {
		markUsedImage(usedImageIDs, tagToID, cont.Image)
	}
	logging.LogDebug(moduleName, fmt.Sprintf("Running containers contributing image keep-set: %d", len(runningContainers)))

	for _, img := range allImages {
		if img.InUse > 0 {
			markUsedImage(usedImageIDs, tagToID, img.ID)
			for _, tag := range img.RepoTags {
				markUsedImage(usedImageIDs, tagToID, tag)
			}
		}
	}

	// Protect images for ALL configured microservices (running + stopped).
	if m.getMicroserviceImages != nil {
		msImages := m.getMicroserviceImages()
		logging.LogInfo(moduleName, fmt.Sprintf("Configured microservice images to protect: %d", len(msImages)))
		for _, imgName := range msImages {
			markUsedImage(usedImageIDs, tagToID, imgName)
		}
	}

	// Collect images not in the protected set.
	toBePruned := make([]string, 0)
	for _, img := range allImages {
		if imageProtected(img, usedImageIDs) {
			continue
		}
		// Use the first tag as the deletion key, fall back to ID.
		key := img.ID
		if len(img.RepoTags) > 0 {
			key = img.RepoTags[0]
		}
		toBePruned = append(toBePruned, key)
	}

	logging.LogInfo(moduleName, fmt.Sprintf("Images total: %d, used: %d, to prune: %d",
		len(allImages), len(usedImageIDs), len(toBePruned)))
	return toBePruned
}

func markUsedImage(used map[string]bool, tagToID map[string]string, ref string) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return
	}
	used[ref] = true
	if id := strings.TrimSpace(tagToID[ref]); id != "" {
		used[id] = true
	}
}

func imageProtected(img engine.ImageInfo, used map[string]bool) bool {
	if used[img.ID] {
		return true
	}
	for _, tag := range img.RepoTags {
		if used[tag] {
			return true
		}
	}
	return img.InUse > 0
}

// PruneAgent prunes dangling images on demand (CLI command / controller API).
// which calls docker system prune
// (dangling-only). This is intentionally lighter than scheduled pruning which
// removes all unused images.
func (m *Manager) PruneAgent() string {
	logging.LogInfo(moduleName, "Initiate prune agent on demand")

	m.mu.Lock()
	eng := m.containerEngine
	m.mu.Unlock()

	ctx := context.Background()
	if eng != nil {
		if _, err := eng.PruneDangling(ctx); err != nil {
			logging.LogError(moduleName, "Error pruning dangling images via container engine", err)
			return "\nFailure - error pruning dangling images"
		}
		logging.LogInfo(moduleName, "Pruned dangling images via container engine")
		return "\nSuccess - pruned dangling images"
	}

	return m.pruneAgentDocker()
}

// ChangePruningFreqInterval reschedules frequency-based pruning with new interval.
// Cancels the old frequency goroutine before launching a new one to prevent leaks.
// Enabling or changing frequency does not prune immediately; the next run is the
// new ticker. Disk-threshold prune is unchanged.
func (m *Manager) ChangePruningFreqInterval() {
	pruningFrequency := m.config.PruningFrequency

	// Cancel the old frequency worker goroutine first.
	if m.freqCancel != nil {
		m.freqCancel()
	}
	if m.frequencyTicker != nil {
		m.frequencyTicker.Stop()
		m.frequencyTicker = nil
	}

	// Create a fresh context for the new worker.
	m.freqCtx, m.freqCancel = context.WithCancel(context.Background())

	if pruningFrequency > 0 {
		duration := time.Duration(pruningFrequency) * time.Hour
		m.frequencyTicker = time.NewTicker(duration)
		go m.frequencyPruningWorker()
		logging.LogInfo(moduleName, fmt.Sprintf("Edgelet pruning frequency updated to: %d hours", pruningFrequency))
	} else {
		logging.LogInfo(moduleName, "Edgelet pruning frequency set to 0 - frequency-based pruning disabled")
	}
}

// GetName returns the module name
func (m *Manager) GetName() string {
	return moduleName
}

// GetModuleIndex returns the module index (Edgelet Pruning Manager doesn't have a specific index)
func (m *Manager) GetModuleIndex() int {
	return -1 // Edgelet Pruning Manager is not tracked in status
}

func isManagedContainer(cont engine.Container) bool {
	return workloadmeta.IsManagedByIofog(cont.Labels)
}
