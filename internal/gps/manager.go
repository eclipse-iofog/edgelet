package gps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	moduleName = "GPS Manager"
)

// Mode represents the GPS mode
type Mode string

const (
	ModeAuto    Mode = "AUTO"
	ModeDynamic Mode = "DYNAMIC"
	ModeManual  Mode = "MANUAL"
	ModeOff     Mode = "OFF"
)

// Manager manages GPS functionality
type Manager struct {
	status          *Status
	deviceHandler   *DeviceHandler
	webHandler      *WebHandler
	isRunning       bool
	mu              sync.RWMutex
	config          *config.Config
	ctx             context.Context
	cancel          context.CancelFunc
	schedulerCancel context.CancelFunc
	updateTicker    *time.Ticker
}

var (
	instance *Manager
	once     sync.Once
)

// GetInstance returns the singleton GPS Manager instance
func GetInstance() *Manager {
	once.Do(func() {
		instance = &Manager{
			status: NewStatus(),
			config: config.GetInstance(),
		}
		instance.ctx, instance.cancel = context.WithCancel(context.Background())
	})
	return instance
}

// Start starts the GPS Manager
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.isRunning {
		return nil
	}

	// Reset context on each start to support supervisor restart cycles
	if m.cancel != nil {
		m.cancel()
	}
	m.ctx, m.cancel = context.WithCancel(context.Background())

	logging.LogInfo(moduleName, "Starting GPS Manager")

	// Start coordinate update scheduler
	m.startCoordinateUpdateScheduler()

	m.isRunning = true
	logging.LogInfo(moduleName, "GPS Manager started successfully")

	// Initialize GPS coordinates asynchronously
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logging.LogError(moduleName, "Panic in GPS initialization", fmt.Errorf("%v", r))
				m.status.SetHealthStatus(HealthStatusIPError)
				if err := m.startOffMode(); err != nil {
					logging.LogWarn(moduleName, fmt.Sprintf("Failed to start off mode: %v", err))
				}
			}
		}()

		logging.LogDebug(moduleName, "Initializing GPS coordinates in background")
		if err := m.initializeGps(); err != nil {
			logging.LogError(moduleName, "Error initializing GPS in background", err)
			m.status.SetHealthStatus(HealthStatusIPError)
			if offErr := m.startOffMode(); offErr != nil {
				logging.LogWarn(moduleName, fmt.Sprintf("Failed to start off mode: %v", offErr))
			}
		}
		logging.LogDebug(moduleName, "GPS coordinates initialization completed")
	}()

	return nil
}

// Stop stops the GPS Manager
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.isRunning {
		return nil
	}

	logging.LogInfo(moduleName, "Stopping GPS Manager")

	m.stopCoordinateUpdateScheduler()

	// Stop device handler if running
	if m.deviceHandler != nil {
		if err := m.deviceHandler.Stop(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("device handler stop error: %v", err))
		}
		m.deviceHandler = nil
	}

	// Stop web handler if running
	if m.webHandler != nil {
		if err := m.webHandler.Stop(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("web handler stop error: %v", err))
		}
		m.webHandler = nil
	}

	if m.cancel != nil {
		m.cancel()
	}

	// Update status to OFF
	m.status.SetHealthStatus(HealthStatusOff)

	m.isRunning = false
	logging.LogInfo(moduleName, "GPS Manager stopped successfully")
	return nil
}

// stopCoordinateUpdateScheduler cancels the active scheduler goroutine and stops its ticker.
// Caller must hold m.mu when invoked from InstanceConfigUpdated/Start/Stop paths.
func (m *Manager) stopCoordinateUpdateScheduler() {
	if m.schedulerCancel != nil {
		m.schedulerCancel()
		m.schedulerCancel = nil
	}
	if m.updateTicker != nil {
		m.updateTicker.Stop()
		m.updateTicker = nil
	}
}

// startCoordinateUpdateScheduler starts the periodic coordinate update scheduler.
// A GPSScanFrequency of 0 (or negative) means GPS scanning is explicitly disabled;
// no scheduler is started and no GPS updates are performed.
func (m *Manager) startCoordinateUpdateScheduler() {
	if m.config.GPSScanFrequency <= 0 {
		logging.LogInfo(moduleName, "GPS scanning disabled (GPSScanFrequency = 0)")
		return
	}

	updateInterval := time.Duration(m.config.GPSScanFrequency) * time.Second
	schedCtx, cancel := context.WithCancel(m.ctx)
	ticker := time.NewTicker(updateInterval)

	m.schedulerCancel = cancel
	m.updateTicker = ticker

	go func(t *time.Ticker, done <-chan struct{}) {
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				m.updateCoordinates()
			}
		}
	}(ticker, schedCtx.Done())
}

// initializeGps initializes GPS based on configured mode
func (m *Manager) initializeGps() error {
	currentMode := m.getGpsMode()
	gpsDevice := m.config.GPSDevice

	logging.LogDebug(moduleName, fmt.Sprintf("Initializing GPS in mode: %s", currentMode))

	switch currentMode {
	case ModeDynamic:
		if gpsDevice != "" {
			return m.startDynamicMode()
		}
		// Fall through to manual if no device
		fallthrough
	case ModeManual:
		return m.startManualMode()
	case ModeAuto:
		return m.initializeAutoMode()
	case ModeOff:
		return m.startOffMode()
	default:
		logging.LogWarn(moduleName, "GPS mode not configured, defaulting to AUTO")
		return m.initializeAutoMode()
	}
}

// getGpsMode gets the GPS mode from config
func (m *Manager) getGpsMode() Mode {
	modeStr := strings.ToUpper(strings.TrimSpace(m.config.GPSMode))
	switch modeStr {
	case string(ModeDynamic):
		return ModeDynamic
	case string(ModeManual):
		return ModeManual
	case string(ModeOff):
		return ModeOff
	default:
		return ModeAuto
	}
}

// initializeAutoMode initializes AUTO mode (IP-based location)
func (m *Manager) initializeAutoMode() error {
	logging.LogDebug(moduleName, "Initializing AUTO mode")
	m.webHandler = NewWebHandler(m)
	return m.webHandler.Start()
}

// startAutoMode starts AUTO mode
func (m *Manager) startAutoMode() error {
	logging.LogDebug(moduleName, "Starting AUTO mode")
	if m.deviceHandler != nil {
		if err := m.deviceHandler.Stop(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("device handler stop error: %v", err))
		}
		m.deviceHandler = nil
	}
	return m.initializeAutoMode()
}

// startDynamicMode starts DYNAMIC mode (device-based)
func (m *Manager) startDynamicMode() error {
	logging.LogDebug(moduleName, "Starting DYNAMIC mode")
	if m.webHandler != nil {
		if err := m.webHandler.Stop(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("web handler stop error: %v", err))
		}
		m.webHandler = nil
	}

	// Create and start device handler
	m.deviceHandler = NewDeviceHandler(m)
	if err := m.deviceHandler.Start(); err != nil {
		logging.LogWarn(moduleName, "Device handler failed, falling back to AUTO mode")
		m.status.SetHealthStatus(HealthStatusDeviceError)
		return m.startAutoMode()
	}

	m.status.SetHealthStatus(HealthStatusHealthy)
	return nil
}

// startManualMode starts MANUAL mode (static coordinates)
func (m *Manager) startManualMode() error {
	logging.LogDebug(moduleName, "Starting MANUAL mode")
	if m.deviceHandler != nil {
		if err := m.deviceHandler.Stop(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("device handler stop error: %v", err))
		}
		m.deviceHandler = nil
	}
	if m.webHandler != nil {
		if err := m.webHandler.Stop(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("web handler stop error: %v", err))
		}
		m.webHandler = nil
	}

	m.status.SetHealthStatus(HealthStatusHealthy)
	// Coordinates are managed in config, no need to set in status
	return nil
}

// GetName returns the module name
func (m *Manager) GetName() string {
	return moduleName
}

// GetModuleIndex returns the module index
func (m *Manager) GetModuleIndex() int {
	return utils.GPSManager
}

// startOffMode starts OFF mode
func (m *Manager) startOffMode() error {
	logging.LogDebug(moduleName, "Starting OFF mode")
	if m.deviceHandler != nil {
		if err := m.deviceHandler.Stop(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("device handler stop error: %v", err))
		}
		m.deviceHandler = nil
	}
	if m.webHandler != nil {
		if err := m.webHandler.Stop(); err != nil {
			logging.LogWarn(moduleName, fmt.Sprintf("web handler stop error: %v", err))
		}
		m.webHandler = nil
	}

	m.status.SetHealthStatus(HealthStatusOff)
	return nil
}

// updateCoordinates updates coordinates based on current mode
func (m *Manager) updateCoordinates() {
	currentMode := m.getGpsMode()
	updated := false

	switch currentMode {
	case ModeDynamic:
		updated = m.updateDynamicCoordinates()
	case ModeAuto:
		updated = m.updateAutoCoordinates()
	case ModeManual, ModeOff:
		// No update needed
	}

	if updated {
		if err := m.config.TriggerGPSConfigCallback(); err != nil {
			logging.LogError(moduleName, "Failed to trigger GPS config callback after coordinate update", err)
		}
	}
}

// updateDynamicCoordinates updates coordinates in DYNAMIC mode.
// A receiver that is talking but has no fix keeps the last device coordinates.
// IP geolocation runs only when the serial port itself cannot be read.
func (m *Manager) updateDynamicCoordinates() bool {
	if m.deviceHandler != nil && m.deviceHandler.IsRunning() {
		previous := m.config.GPSCoordinates
		err := m.deviceHandler.ReadAndUpdateCoordinates()
		if err == nil {
			m.noteHealth(HealthStatusHealthy)
			return m.config.GPSCoordinates != previous
		}
		if errors.Is(err, errNoFix) {
			m.noteHealth(HealthStatusNoFix)
			return false
		}
		logging.LogError(moduleName, "Error updating DYNAMIC coordinates", err)
		m.noteHealth(HealthStatusDeviceError)
		return m.fallbackToIP()
	}
	logging.LogWarn(moduleName, "Device handler not running, falling back to AUTO mode")
	m.noteHealth(HealthStatusDeviceError)
	return m.fallbackToIP()
}

// updateAutoCoordinates updates coordinates in AUTO mode.
func (m *Manager) updateAutoCoordinates() bool {
	if m.webHandler == nil {
		return false
	}
	previous := m.config.GPSCoordinates
	if err := m.webHandler.UpdateCoordinates(); err != nil {
		logging.LogError(moduleName, "Error updating AUTO coordinates", err)
		m.noteHealth(HealthStatusIPError)
		return false
	}
	m.noteHealth(HealthStatusHealthy)
	return m.config.GPSCoordinates != previous
}

// fallbackToIP looks up coordinates when the GPS device cannot be read.
// A successful lookup leaves the device-error status in place.
func (m *Manager) fallbackToIP() bool {
	if m.webHandler == nil {
		m.webHandler = NewWebHandler(m)
	}
	previous := m.config.GPSCoordinates
	if err := m.webHandler.UpdateCoordinates(); err != nil {
		logging.LogError(moduleName, "Error updating AUTO coordinates", err)
		m.noteHealth(HealthStatusIPError)
		return false
	}
	return m.config.GPSCoordinates != previous
}

func (m *Manager) noteHealth(status HealthStatus) {
	if m.status == nil {
		return
	}
	previous := m.status.GetHealthStatus()
	if previous == status {
		return
	}
	m.status.SetHealthStatus(status)
	switch status {
	case HealthStatusNoFix:
		logging.LogInfo(moduleName, "GPS no fix; keeping last coordinates")
	case HealthStatusHealthy:
		if previous == HealthStatusNoFix {
			logging.LogInfo(moduleName, "GPS fix acquired")
		}
	}
}

// GetStatus returns the GPS status
func (m *Manager) GetStatus() *Status {
	return m.status
}

// InstanceConfigUpdated handles configuration updates
func (m *Manager) InstanceConfigUpdated() {
	m.mu.Lock()
	defer m.mu.Unlock()

	logging.LogDebug(moduleName, "Handling GPS configuration update")

	m.stopCoordinateUpdateScheduler()

	// Re-initialize GPS mode with new settings
	if err := m.initializeGps(); err != nil {
		logging.LogError(moduleName, "Error re-initializing GPS on config update", err)
	}

	// Restart coordinate update scheduler with (potentially new) frequency
	m.startCoordinateUpdateScheduler()
}
