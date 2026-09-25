package version

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	moduleName           = "Version Handler"
	otaInProgressTimeout = 60 * time.Second
	defaultOTAInstallLog = "/var/log/edgelet/ota-install.log"
)

var otaInstallLogPath = defaultOTAInstallLog

func setOTAInstallLogPath(path string) {
	if strings.TrimSpace(path) == "" {
		otaInstallLogPath = defaultOTAInstallLog
		return
	}
	otaInstallLogPath = path
}

// VersionCommand represents a version change command.
type VersionCommand string //nolint:revive // exported API

const (
	VersionCommandUpgrade  VersionCommand = "UPGRADE"
	VersionCommandRollback VersionCommand = "ROLLBACK"
)

// ParseVersionCommand parses version command from JSON.
func ParseVersionCommand(actionData map[string]any) (VersionCommand, error) {
	cmdStr, ok := actionData["command"].(string)
	if !ok {
		return "", errors.New("command not found in action data")
	}

	cmd := VersionCommand(strings.ToUpper(cmdStr))
	switch cmd {
	case VersionCommandUpgrade, VersionCommandRollback:
		return cmd, nil
	default:
		return "", fmt.Errorf("unknown version command: %s", cmdStr)
	}
}

// RefreshFunc fetches a fresh controller version payload (used for OTA key refresh).
type RefreshFunc func() (map[string]any, error)

// Handler orchestrates controller-driven OTA via install.sh.
type Handler struct {
	manager         *ReleaseManager
	isContainer     func() bool
	isDaemonHealthy func() bool
	startDetached   func(script string, args ...string) error
	refreshVersion  RefreshFunc

	mu       sync.Mutex
	otaUntil time.Time
}

var (
	instance *Handler
	once     sync.Once
)

// NewHandler constructs a Handler with the given ReleaseManager.
func NewHandler(manager *ReleaseManager) *Handler {
	if manager == nil {
		manager = NewReleaseManager()
	}
	return &Handler{
		manager:         manager,
		isContainer:     defaultIsContainer,
		isDaemonHealthy: defaultDaemonHealthy,
		startDetached:   defaultStartDetached,
	}
}

// GetInstance returns the singleton version handler.
func GetInstance() *Handler {
	once.Do(func() {
		instance = NewHandler(NewReleaseManager())
	})
	return instance
}

// SetVersionRefreshFunc configures a callback to re-fetch controller version metadata.
func (h *Handler) SetVersionRefreshFunc(fn RefreshFunc) {
	if h == nil {
		return
	}
	h.refreshVersion = fn
}

// ChangeVersion performs a controller-initiated upgrade or rollback.
func (h *Handler) ChangeVersion(actionData map[string]any) error {
	logging.LogInfo(moduleName, "Start performing change version operation, received from ioFog controller")

	if h.isContainer() {
		logging.LogWarn(moduleName, "Edgelet daemon is running inside container; upgrade/rollback via orchestrator image rollout")
		return nil
	}

	if runtime.GOOS == "windows" {
		logging.LogWarn(moduleName, "Windows OTA is not supported by the version handler")
		return nil
	}

	command, err := ParseVersionCommand(actionData)
	if err != nil {
		logging.LogError(moduleName, "Error performing change version operation: invalid command", err)
		return err
	}

	provisionKey, ok := actionData["provisionKey"].(string)
	if !ok {
		provisionKey = ""
	}

	if h.isValidChangeVersionOperation(command, actionData) {
		if err := h.executeChangeVersionScript(command, actionData, provisionKey); err != nil {
			return err
		}
	}

	logging.LogInfo(moduleName, "Finished performing change version operation, received from ioFog controller")
	return nil
}

func (h *Handler) executeChangeVersionScript(command VersionCommand, actionData map[string]any, provisionKey string) error {
	actionData, provisionKey, err := h.refreshVersionActionIfNeeded(actionData, provisionKey)
	if err != nil {
		return err
	}

	if provisionKey != "" {
		pending, err := PendingFromAction(actionData)
		if err != nil {
			return fmt.Errorf("OTA reprovision pending: %w", err)
		}
		if err := WriteOTAReprovisionPending(
			pending.ProvisionKey,
			pending.Command,
			pending.TargetVersion,
			pending.ExpirationTime,
		); err != nil {
			return fmt.Errorf("write OTA reprovision pending: %w", err)
		}
		logging.LogInfo(moduleName, "Wrote OTA reprovision pending file before install.sh")
	}

	script := h.manager.InstallScriptPath()
	args := make([]string, 0, 3)
	switch command {
	case VersionCommandUpgrade:
		args = append(args, "--upgrade")
		if target := targetVersionFromAction(actionData); target != "" {
			args = append(args, "--version="+versionForInstallScript(target))
		}
	case VersionCommandRollback:
		args = append(args, "--rollback")
	default:
		return fmt.Errorf("no install.sh flag for command: %s", command)
	}

	logging.LogInfo(moduleName, fmt.Sprintf("Start detached install.sh %s", strings.Join(args, " ")))
	if err := h.startDetached(script, args...); err != nil {
		logging.LogError(moduleName, "Error executing install.sh for version change", err)
		return err
	}
	h.markOTAInProgress()
	logging.LogInfo(moduleName, "Finished launching install.sh for version change")
	return nil
}

func (h *Handler) refreshVersionActionIfNeeded(actionData map[string]any, provisionKey string) (map[string]any, string, error) {
	if provisionKey == "" {
		return actionData, provisionKey, nil
	}

	pending, err := PendingFromAction(actionData)
	if err != nil {
		return actionData, provisionKey, err
	}
	if !pending.NeedsPreflightRefresh(time.Now()) {
		return actionData, provisionKey, nil
	}
	if h.refreshVersion == nil {
		logging.LogWarn(moduleName, "OTA provision key near expiry but version refresh callback is unset")
		return actionData, provisionKey, nil
	}

	raw, err := h.refreshVersion()
	if err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Pre-flight version refresh failed: %v", err))
		return actionData, provisionKey, nil
	}

	refreshed, err := NormalizeVersionResponse(raw)
	if err != nil {
		logging.LogWarn(moduleName, fmt.Sprintf("Pre-flight version refresh normalize failed: %v", err))
		return actionData, provisionKey, nil
	}
	if refreshed == nil {
		return actionData, provisionKey, nil
	}

	key, ok := refreshed["provisionKey"].(string)
	if !ok || strings.TrimSpace(key) == "" {
		return actionData, provisionKey, nil
	}

	logging.LogInfo(moduleName, "Refreshed OTA provision key before install.sh")
	return refreshed, key, nil
}

func (h *Handler) isValidChangeVersionOperation(command VersionCommand, actionData map[string]any) bool {
	switch command {
	case VersionCommandUpgrade:
		return h.IsReadyToUpgradeWithAction(actionData)
	case VersionCommandRollback:
		return h.IsReadyToRollbackWithAction(actionData)
	default:
		return false
	}
}

// IsReadyToUpgrade reports upgrade readiness using GitHub latest as target.
func (h *Handler) IsReadyToUpgrade() bool {
	return h.IsReadyToUpgradeWithAction(nil)
}

// IsReadyToUpgradeWithAction reports upgrade readiness for an optional controller target.
func (h *Handler) IsReadyToUpgradeWithAction(actionData map[string]any) bool {
	logging.LogDebug(moduleName, "Checking if ready to upgrade")

	if h.otaInProgress() {
		logging.LogDebug(moduleName, "Is ready to upgrade: false (OTA in progress)")
		return false
	}

	if h.isContainer() {
		ready := h.containerUpgradeReady(actionData)
		logging.LogDebug(moduleName, fmt.Sprintf("Is ready to upgrade (container): %v", ready))
		return ready
	}

	if runtime.GOOS == "windows" {
		return false
	}

	if !h.manager.InstallScriptExists() {
		logging.LogDebug(moduleName, "Is ready to upgrade: false (install.sh missing)")
		return false
	}

	if !h.isDaemonHealthy() {
		logging.LogDebug(moduleName, "Is ready to upgrade: false (daemon not healthy)")
		return false
	}

	target, err := h.manager.GetCandidateVersion(actionData)
	if err != nil || target == "" {
		logging.LogDebug(moduleName, fmt.Sprintf("Is ready to upgrade: false (target unavailable: %v)", err))
		return false
	}

	installed := h.manager.GetInstalledVersion()
	ready := installed != "" && installed != target
	logging.LogDebug(moduleName, fmt.Sprintf("Is ready to upgrade: %v (installed=%s target=%s)", ready, installed, target))
	return ready
}

func (h *Handler) containerUpgradeReady(actionData map[string]any) bool {
	target, err := h.manager.GetCandidateVersion(actionData)
	if err != nil || target == "" {
		return false
	}
	installed := normalizeVersion(h.manager.runningVersion())
	return installed != "" && installed != target
}

// IsReadyToRollback reports rollback readiness from previous-release and cache/url.
func (h *Handler) IsReadyToRollback() bool {
	return h.IsReadyToRollbackWithAction(nil)
}

// IsReadyToRollbackWithAction reports rollback readiness for an optional controller target.
func (h *Handler) IsReadyToRollbackWithAction(actionData map[string]any) bool {
	logging.LogDebug(moduleName, "Checking is ready to rollback")

	if h.otaInProgress() {
		logging.LogDebug(moduleName, "Is ready to rollback: false (OTA in progress)")
		return false
	}

	if h.isContainer() {
		logging.LogDebug(moduleName, "Is ready to rollback: false (container uses orchestrator rollout)")
		return false
	}

	if runtime.GOOS == "windows" {
		return false
	}

	if !h.manager.PreviousReleaseExists() {
		logging.LogDebug(moduleName, "Is ready to rollback: false (previous-release missing)")
		return false
	}

	prev, err := h.manager.ReadPreviousRelease()
	if err != nil || prev == nil || strings.TrimSpace(prev.PreviousVersion) == "" {
		logging.LogDebug(moduleName, "Is ready to rollback: false (previous-release unreadable)")
		return false
	}

	if target := targetVersionFromAction(actionData); target != "" {
		if normalizeVersion(prev.PreviousVersion) != normalizeVersion(target) {
			logging.LogDebug(moduleName, fmt.Sprintf(
				"Is ready to rollback: false (semver/target %s != previous %s)",
				target, prev.PreviousVersion,
			))
			return false
		}
	}

	osName := prev.PreviousOS
	arch := prev.PreviousArch
	if osName == "" {
		osName = "linux"
	}
	if arch == "" {
		arch = runtime.GOARCH
	}

	hasCache := h.manager.HasCachedBinary(prev.PreviousVersion, osName, arch)
	hasURL := strings.TrimSpace(prev.PreviousDownloadURL) != ""
	reachable := hasURL && h.manager.IsPreviousDownloadReachable(prev.PreviousDownloadURL)
	ready := hasCache || reachable

	logging.LogDebug(moduleName, fmt.Sprintf(
		"Is ready to rollback: %v (cache=%v url=%v reachable=%v)",
		ready, hasCache, hasURL, reachable,
	))
	return ready
}

func (h *Handler) markOTAInProgress() {
	h.mu.Lock()
	h.otaUntil = time.Now().Add(otaInProgressTimeout)
	h.mu.Unlock()
}

func (h *Handler) otaInProgress() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return time.Now().Before(h.otaUntil)
}

func defaultIsContainer() bool {
	return strings.EqualFold(os.Getenv("EDGELET_DAEMON"), "container")
}

func defaultDaemonHealthy() bool {
	return statusreporter.GetInstance().DaemonOperational()
}

func defaultStartDetached(script string, args ...string) error {
	logFile, err := openOTAInstallLog()
	if err != nil {
		return err
	}
	cmdArgs := append([]string{script}, args...)
	cmd := exec.Command("sh", cmdArgs...) // #nosec G204 -- script path is fixed contract path; args are validated flags
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	setDetachedProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	report := reportDetachedInstallExit
	go func() {
		err := cmd.Wait()
		_ = logFile.Close()
		report(err)
	}()
	return nil
}

func openOTAInstallLog() (*os.File, error) {
	path := otaInstallLogPath
	if strings.TrimSpace(path) == "" {
		path = defaultOTAInstallLog
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create OTA install log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640) // #nosec G302 G304 -- OTA install log is a fixed operator path
	if err != nil {
		return nil, fmt.Errorf("open OTA install log: %w", err)
	}
	return f, nil
}

func logDetachedInstallExit(err error) {
	if err == nil {
		return
	}
	logging.LogError(moduleName, "install.sh exited with an error", err)
}

var reportDetachedInstallExit = logDetachedInstallExit
