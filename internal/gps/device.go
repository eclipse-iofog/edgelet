package gps

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/gps/nmea"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	deviceHandlerModuleName = "GPS Device Handler"
	defaultReadTimeout      = 5 * time.Second
	defaultDrainIdle        = 50 * time.Millisecond
	maxDrainLines           = 128
	maxNMEALineLength       = 512
)

var (
	errNoFix         = errors.New("gps fix unavailable")
	errReadTimeout   = errors.New("read timeout")
	errOversizedLine = errors.New("nmea line exceeds maximum length")
)

// DeviceHandler handles GPS device communication
type DeviceHandler struct {
	manager     *Manager
	config      *config.Config
	devicePath  string
	deviceFile  *os.File
	reader      *bufio.Reader
	isRunning   bool
	readTimeout time.Duration
	idleTimeout time.Duration
	mu          sync.RWMutex
}

// NewDeviceHandler creates a new DeviceHandler
func NewDeviceHandler(manager *Manager) *DeviceHandler {
	return &DeviceHandler{
		manager: manager,
		config:  config.GetInstance(),
	}
}

// Start starts the device handler
func (d *DeviceHandler) Start() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.isRunning {
		return nil
	}

	devicePath := d.config.GPSDevice
	if devicePath == "" {
		return errors.New("GPS device not configured")
	}

	logging.LogDebug(deviceHandlerModuleName, fmt.Sprintf("Starting GPS device handler: %s", devicePath))

	// Open device file
	file, err := os.OpenFile(filepath.Clean(devicePath), os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("failed to open GPS device: %w", err)
	}

	d.devicePath = devicePath
	d.deviceFile = file
	d.reader = bufio.NewReader(file)
	d.isRunning = true

	logging.LogInfo(deviceHandlerModuleName, fmt.Sprintf("GPS device handler started: %s", devicePath))
	return nil
}

// Stop stops the device handler
func (d *DeviceHandler) Stop() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.isRunning {
		return nil
	}

	logging.LogDebug(deviceHandlerModuleName, "Stopping GPS device handler")

	if d.deviceFile != nil {
		if err := d.deviceFile.Close(); err != nil {
			logging.LogWarn(deviceHandlerModuleName, fmt.Sprintf("Failed to close GPS device file: %v", err))
		}
		d.deviceFile = nil
	}
	d.reader = nil
	d.isRunning = false

	logging.LogInfo(deviceHandlerModuleName, "GPS device handler stopped")
	return nil
}

// IsRunning returns whether the device handler is running
func (d *DeviceHandler) IsRunning() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.isRunning
}

// ReadAndUpdateCoordinates reads the sentences already buffered for this scan.
// A valid fix updates coordinates. Sentences with no fix return errNoFix and
// leave the stored coordinates unchanged. No bytes, or a port error, is returned
// to the caller as a device failure.
func (d *DeviceHandler) ReadAndUpdateCoordinates() error {
	d.mu.RLock()
	if !d.isRunning || d.reader == nil {
		d.mu.RUnlock()
		return errors.New("device handler not running")
	}
	reader := d.reader
	deviceFile := d.deviceFile
	firstTimeout := d.firstReadTimeout()
	idleTimeout := d.drainIdleTimeout()
	d.mu.RUnlock()
	if deviceFile == nil {
		return errors.New("device file is not open")
	}

	var last nmea.Message
	var readErr error
	lines := 0
	for lines < maxDrainLines {
		timeout := idleTimeout
		if lines == 0 {
			timeout = firstTimeout
		}
		line, err := d.nextLine(deviceFile, reader, timeout)
		if err != nil {
			if errors.Is(err, errOversizedLine) {
				lines++
				continue
			}
			readErr = err
			break
		}
		lines++
		msg, err := nmea.Parse(line)
		if err != nil {
			continue
		}
		last = msg
	}

	if lines == 0 {
		if readErr == nil {
			readErr = errReadTimeout
		}
		logging.LogWarn(deviceHandlerModuleName, fmt.Sprintf("GPS device read failed: %v", readErr))
		return readErr
	}
	if last != nil && last.IsValid() {
		coordinates := fmt.Sprintf("%.5f,%.5f", last.GetLatitude(), last.GetLongitude())
		d.config.GPSCoordinates = coordinates
		logging.LogDebug(deviceHandlerModuleName, fmt.Sprintf("Updated GPS coordinates: %s", coordinates))
		return nil
	}
	if readErr != nil && !errors.Is(readErr, errReadTimeout) {
		logging.LogWarn(deviceHandlerModuleName, fmt.Sprintf("GPS device read failed: %v", readErr))
		return readErr
	}
	return errNoFix
}

func (d *DeviceHandler) firstReadTimeout() time.Duration {
	if d.readTimeout > 0 {
		return d.readTimeout
	}
	return defaultReadTimeout
}

func (d *DeviceHandler) drainIdleTimeout() time.Duration {
	if d.idleTimeout > 0 {
		return d.idleTimeout
	}
	return defaultDrainIdle
}
