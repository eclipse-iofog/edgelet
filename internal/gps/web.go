package gps

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	webHandlerModuleName = "GPS Web Handler"
	defaultIPAPIURL      = "https://ipv4-check-perf.radar.cloudflare.com/api/info"
	timeout              = 10 * time.Second
	maxLookupBodyBytes   = 64 * 1024
)

var ipAPIURL = defaultIPAPIURL

// WebHandler handles IP-based GPS location
type WebHandler struct {
	manager *Manager
	config  *config.Config
	client  *http.Client
}

// NewWebHandler creates a new WebHandler
func NewWebHandler(manager *Manager) *WebHandler {
	return &WebHandler{
		manager: manager,
		config:  config.GetInstance(),
		client: &http.Client{
			Timeout: timeout,
		},
	}
}

// Start starts the web handler. The first lookup runs in the background so GPS
// startup and config reload do not wait on the network.
func (w *WebHandler) Start() error {
	logging.LogDebug(webHandlerModuleName, "Starting GPS Web Handler")
	go w.refreshCoordinates()
	return nil
}

func (w *WebHandler) refreshCoordinates() {
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(webHandlerModuleName, "Panic while updating GPS coordinates", fmt.Errorf("%v", r))
		}
	}()
	if err := w.UpdateCoordinates(); err != nil {
		logging.LogError(webHandlerModuleName, "Error updating AUTO coordinates", err)
		if w.manager != nil && w.manager.status != nil {
			w.manager.status.SetHealthStatus(HealthStatusIPError)
		}
		return
	}
	if w.manager != nil && w.manager.status != nil {
		w.manager.status.SetHealthStatus(HealthStatusHealthy)
	}
}

// Stop stops the web handler
func (w *WebHandler) Stop() error {
	logging.LogDebug(webHandlerModuleName, "Stopping GPS Web Handler")
	return nil
}

// UpdateCoordinates updates AUTO-mode coordinates from the IP location service.
// It does not change the external IP address. On failure the previous coordinates stay in place.
func (w *WebHandler) UpdateCoordinates() error {
	logging.LogDebug(webHandlerModuleName, "Updating coordinates from IP-based location service")

	parent := context.Background()
	if w.manager != nil && w.manager.ctx != nil {
		parent = w.manager.ctx
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	body, err := readLookupJSON(ctx, w.client, ipAPIURL)
	if err != nil {
		return err
	}

	var locationData struct {
		Latitude  *flexFloat `json:"latitude"`
		Longitude *flexFloat `json:"longitude"`
	}
	if err := json.Unmarshal(body, &locationData); err != nil {
		return fmt.Errorf("failed to parse location response: %w", err)
	}
	if locationData.Latitude == nil || locationData.Longitude == nil {
		return errors.New("location provider missing latitude/longitude fields")
	}
	lat := float64(*locationData.Latitude)
	lon := float64(*locationData.Longitude)
	if !validCoordinates(lat, lon) {
		return fmt.Errorf("location provider returned coordinates out of range: %f,%f", lat, lon)
	}

	coordinates := fmt.Sprintf("%.5f,%.5f", lat, lon)
	w.config.GPSCoordinates = coordinates

	logging.LogDebug(webHandlerModuleName, fmt.Sprintf("Updated GPS coordinates: %s", coordinates))
	return nil
}

func validCoordinates(lat, lon float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
		return false
	}
	return lat >= -90 && lat <= 90 && lon >= -180 && lon <= 180
}

// flexFloat accepts a JSON number or a numeric string.
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		return errors.New("empty coordinate")
	}
	if data[0] == '"' {
		var raw string
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return err
		}
		*f = flexFloat(value)
		return nil
	}
	var value float64
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*f = flexFloat(value)
	return nil
}

func readLookupJSON(ctx context.Context, client *http.Client, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create location request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get location: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected location status code: %d", resp.StatusCode)
	}
	if ct := strings.ToLower(resp.Header.Get("Content-Type")); ct != "" && !strings.Contains(ct, "json") {
		return nil, fmt.Errorf("location provider returned non-JSON content type %q", resp.Header.Get("Content-Type"))
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLookupBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to read location response: %w", err)
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] == '<' {
		return nil, errors.New("location provider returned an unexpected response")
	}
	return trimmed, nil
}

// GetCoordinates returns the current coordinates
func (w *WebHandler) GetCoordinates() string {
	coords := w.config.GPSCoordinates
	if coords == "" {
		return "0.00000,0.00000"
	}
	return coords
}

// ParseCoordinates parses coordinates string "lat,lon" into latitude and longitude
func ParseCoordinates(coords string) (float64, float64, error) {
	parts := strings.Split(coords, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid coordinates format: %s", coords)
	}

	var lat, lon float64
	if _, err := fmt.Sscanf(parts[0], "%f", &lat); err != nil {
		return 0, 0, fmt.Errorf("invalid latitude: %w", err)
	}
	if _, err := fmt.Sscanf(parts[1], "%f", &lon); err != nil {
		return 0, 0, fmt.Errorf("invalid longitude: %w", err)
	}

	return lat, lon, nil
}
