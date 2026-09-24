package gps

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
)

func TestWebHandlerUpdateCoordinates_ParsesStringLatLon(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"latitude":"41.01384","longitude":"28.94966","ip_address":"203.0.113.10"}`))
	}))
	defer func() {
		server.Close()
	}()

	prevURL := ipAPIURL
	ipAPIURL = server.URL
	t.Cleanup(func() { ipAPIURL = prevURL })

	cfg := config.GetInstance()
	cfg.GPSCoordinates = ""
	cfg.IPAddressExternal = "198.51.100.20"

	handler := NewWebHandler(nil)
	if err := handler.UpdateCoordinates(); err != nil {
		t.Fatalf("UpdateCoordinates returned error: %v", err)
	}
	if cfg.GPSCoordinates != "41.01384,28.94966" {
		t.Fatalf("unexpected coordinates: %s", cfg.GPSCoordinates)
	}
	if cfg.GetIPAddressExternal() != "198.51.100.20" {
		t.Fatalf("location lookup changed external IP: %s", cfg.GetIPAddressExternal())
	}
}

func TestWebHandlerUpdateCoordinates_MissingLatLonFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip_address":"203.0.113.10"}`))
	}))
	defer func() {
		server.Close()
	}()

	prevURL := ipAPIURL
	ipAPIURL = server.URL
	t.Cleanup(func() { ipAPIURL = prevURL })

	cfg := config.GetInstance()
	cfg.GPSCoordinates = "10.00000,20.00000"

	handler := NewWebHandler(nil)
	if err := handler.UpdateCoordinates(); err == nil {
		t.Fatal("expected UpdateCoordinates to fail when latitude/longitude are missing")
	}
	if cfg.GPSCoordinates != "10.00000,20.00000" {
		t.Fatalf("coordinates should remain unchanged on failure, got %s", cfg.GPSCoordinates)
	}
}

func TestWebHandlerUpdateCoordinates_HTMLChallengeKeepsPrevious(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>Just a moment...</body></html>`))
	}))
	defer func() {
		server.Close()
	}()

	prevURL := ipAPIURL
	ipAPIURL = server.URL
	t.Cleanup(func() { ipAPIURL = prevURL })

	cfg := config.GetInstance()
	cfg.GPSCoordinates = "10.00000,20.00000"

	handler := NewWebHandler(nil)
	if err := handler.UpdateCoordinates(); err == nil {
		t.Fatal("expected UpdateCoordinates to fail on an HTML challenge")
	}
	if cfg.GPSCoordinates != "10.00000,20.00000" {
		t.Fatalf("coordinates should remain unchanged on failure, got %s", cfg.GPSCoordinates)
	}
}

func TestWebHandlerUpdateCoordinates_OutOfRangeKeepsPrevious(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"latitude":"91","longitude":"28.94966"}`))
	}))
	defer func() {
		server.Close()
	}()

	prevURL := ipAPIURL
	ipAPIURL = server.URL
	t.Cleanup(func() { ipAPIURL = prevURL })

	cfg := config.GetInstance()
	cfg.GPSCoordinates = "10.00000,20.00000"

	handler := NewWebHandler(nil)
	if err := handler.UpdateCoordinates(); err == nil {
		t.Fatal("expected UpdateCoordinates to fail when latitude is out of range")
	}
	if cfg.GPSCoordinates != "10.00000,20.00000" {
		t.Fatalf("coordinates should remain unchanged on failure, got %s", cfg.GPSCoordinates)
	}
}
