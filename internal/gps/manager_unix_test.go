//go:build unix

package gps

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
)

func TestUpdateDynamicCoordinates_NoFixSkipsIPAndKeepsCoordinates(t *testing.T) {
	cfg := config.GetInstance()
	cfg.GPSMode = "dynamic"
	cfg.GPSCoordinates = "40.99058,29.05742"
	t.Cleanup(func() { cfg.SetGPSConfigCallback(nil) })

	var hits int32
	var callbacks int32
	server := ipLookupServer(t, &hits)
	restoreIPAPI(t, server.URL)
	cfg.SetGPSConfigCallback(func() error {
		atomic.AddInt32(&callbacks, 1)
		return nil
	})

	handler, writer := testDeviceHandler(t, 200*time.Millisecond)
	writeNMEA(t, writer, "$GNRMC,132843.00,V,,,,,,,,,,N*6C")

	m := &Manager{
		status:        NewStatus(),
		config:        cfg,
		deviceHandler: handler,
	}
	m.updateCoordinates()
	if got := atomic.LoadInt32(&hits); got != 0 {
		t.Fatalf("IP lookup calls = %d, want 0", got)
	}
	if got := atomic.LoadInt32(&callbacks); got != 0 {
		t.Fatalf("GPS callbacks = %d, want 0", got)
	}
	if cfg.GPSCoordinates != "40.99058,29.05742" {
		t.Fatalf("coordinates changed to %s", cfg.GPSCoordinates)
	}
	if m.status.GetHealthStatus() != HealthStatusNoFix {
		t.Fatalf("status %s, want NO_FIX", m.status.GetHealthStatus())
	}
}

func TestUpdateDynamicCoordinates_RepeatedFixPostsOnce(t *testing.T) {
	cfg := config.GetInstance()
	cfg.GPSMode = "dynamic"
	cfg.GPSCoordinates = "0.00000,0.00000"
	t.Cleanup(func() { cfg.SetGPSConfigCallback(nil) })

	var callbacks int32
	cfg.SetGPSConfigCallback(func() error {
		atomic.AddInt32(&callbacks, 1)
		return nil
	})

	handler, writer := testDeviceHandler(t, 200*time.Millisecond)
	fix := "$GNRMC,132909.00,A,4059.43459,N,02903.44495,E,0.189,,131026,,,A*6E"
	writeNMEA(t, writer, fix)

	m := &Manager{
		status:        NewStatus(),
		config:        cfg,
		deviceHandler: handler,
	}
	m.updateCoordinates()
	if cfg.GPSCoordinates != "40.99058,29.05742" {
		t.Fatalf("coordinates %s", cfg.GPSCoordinates)
	}
	if m.status.GetHealthStatus() != HealthStatusHealthy {
		t.Fatalf("status %s, want HEALTHY", m.status.GetHealthStatus())
	}

	writeNMEA(t, writer, fix)
	m.updateCoordinates()
	if got := atomic.LoadInt32(&callbacks); got != 1 {
		t.Fatalf("GPS callbacks = %d, want 1", got)
	}
}

func TestUpdateDynamicCoordinates_SilentPortUsesIPFallback(t *testing.T) {
	cfg := config.GetInstance()
	cfg.GPSMode = "dynamic"
	cfg.GPSCoordinates = "40.99058,29.05742"
	t.Cleanup(func() { cfg.SetGPSConfigCallback(nil) })

	var hits int32
	var callbacks int32
	server := ipLookupServer(t, &hits)
	restoreIPAPI(t, server.URL)
	cfg.SetGPSConfigCallback(func() error {
		atomic.AddInt32(&callbacks, 1)
		return nil
	})

	handler, _ := testDeviceHandler(t, 40*time.Millisecond)
	m := &Manager{
		status:        NewStatus(),
		config:        cfg,
		deviceHandler: handler,
	}
	m.updateCoordinates()
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("IP lookup calls = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&callbacks); got != 1 {
		t.Fatalf("GPS callbacks = %d, want 1", got)
	}
	if cfg.GPSCoordinates != "41.01510,28.97950" {
		t.Fatalf("coordinates %s, want IP fallback", cfg.GPSCoordinates)
	}
	if m.status.GetHealthStatus() != HealthStatusDeviceError {
		t.Fatalf("status %s, want DEVICE_ERROR", m.status.GetHealthStatus())
	}
}

func TestUpdateDynamicCoordinates_UnsupportedSentenceThenFix(t *testing.T) {
	cfg := config.GetInstance()
	cfg.GPSMode = "dynamic"
	cfg.GPSCoordinates = "0.00000,0.00000"

	handler, writer := testDeviceHandler(t, 200*time.Millisecond)
	writeNMEA(t, writer,
		checksummed("GNGSA,A,1,,,,,,,,,,,,,99.99,99.99,99.99"),
		"$GNGGA,132909.00,4059.43459,N,02903.44495,E,1,06,3.38,38.8,M,37.5,M,,*79",
	)
	m := &Manager{
		status:        NewStatus(),
		config:        cfg,
		deviceHandler: handler,
	}
	if updated := m.updateDynamicCoordinates(); !updated {
		t.Fatal("GGA fix after GSA should update coordinates")
	}
	if cfg.GPSCoordinates != "40.99058,29.05742" {
		t.Fatalf("coordinates %s", cfg.GPSCoordinates)
	}
}

func ipLookupServer(t *testing.T, hits *int32) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"latitude":"41.01510","longitude":"28.97950"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

func restoreIPAPI(t *testing.T, lookupURL string) {
	t.Helper()
	original := ipAPIURL
	ipAPIURL = lookupURL
	t.Cleanup(func() { ipAPIURL = original })
}

func checksummed(body string) string {
	var sum byte
	for i := 0; i < len(body); i++ {
		sum ^= body[i]
	}
	return fmt.Sprintf("$%s*%02X", body, sum)
}
