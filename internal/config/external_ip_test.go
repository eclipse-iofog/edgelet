package config

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// Config reload tests must not dial the public lookup.
	runExternalIPRefresh = func() {}
	m.Run()
}

func TestRefreshIPAddressExternal_ParsesAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"latitude":"41.01384","longitude":"28.94966","ip_address":"203.0.113.10"}`))
	}))
	defer server.Close()

	prevURL := externalIPLookupURL
	externalIPLookupURL = server.URL
	t.Cleanup(func() { externalIPLookupURL = prevURL })

	cfg := GetInstance()
	cfg.GPSCoordinates = "10.00000,20.00000"
	cfg.setIPAddressExternal("")

	if err := RefreshIPAddressExternal(context.Background()); err != nil {
		t.Fatalf("RefreshIPAddressExternal returned error: %v", err)
	}
	if cfg.GetIPAddressExternal() != "203.0.113.10" {
		t.Fatalf("unexpected external IP: %s", cfg.GetIPAddressExternal())
	}
	if cfg.GPSCoordinates != "10.00000,20.00000" {
		t.Fatalf("external IP lookup changed GPS coordinates: %s", cfg.GPSCoordinates)
	}
}

func TestRefreshIPAddressExternal_InvalidAddressKeepsPrevious(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip_address":"not-an-ip","latitude":"1","longitude":"2"}`))
	}))
	defer server.Close()

	prevURL := externalIPLookupURL
	externalIPLookupURL = server.URL
	t.Cleanup(func() { externalIPLookupURL = prevURL })

	cfg := GetInstance()
	cfg.GPSCoordinates = "10.00000,20.00000"
	cfg.setIPAddressExternal("198.51.100.8")

	if err := RefreshIPAddressExternal(context.Background()); err == nil {
		t.Fatal("expected RefreshIPAddressExternal to fail on an invalid address")
	}
	if cfg.GetIPAddressExternal() != "198.51.100.8" {
		t.Fatalf("external IP changed on failure: %s", cfg.GetIPAddressExternal())
	}
	if cfg.GPSCoordinates != "10.00000,20.00000" {
		t.Fatalf("GPS coordinates changed on failure: %s", cfg.GPSCoordinates)
	}
}

func TestRefreshIPAddressExternal_HTMLChallengeKeepsPrevious(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<!DOCTYPE html><html><body>Just a moment...</body></html>`))
	}))
	defer server.Close()

	prevURL := externalIPLookupURL
	externalIPLookupURL = server.URL
	t.Cleanup(func() { externalIPLookupURL = prevURL })

	cfg := GetInstance()
	cfg.setIPAddressExternal("198.51.100.8")

	if err := RefreshIPAddressExternal(context.Background()); err == nil {
		t.Fatal("expected RefreshIPAddressExternal to fail on an HTML challenge")
	}
	if cfg.GetIPAddressExternal() != "198.51.100.8" {
		t.Fatalf("external IP changed on failure: %s", cfg.GetIPAddressExternal())
	}
}

func TestScheduleIPAddressExternalRefresh_DoesNotBlock(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ip_address":"203.0.113.9"}`))
	}))
	defer server.Close()

	prevURL := externalIPLookupURL
	prevRun := runExternalIPRefresh
	externalIPLookupURL = server.URL
	runExternalIPRefresh = func() {
		_ = RefreshIPAddressExternal(context.Background())
	}
	t.Cleanup(func() {
		externalIPLookupURL = prevURL
		runExternalIPRefresh = prevRun
		select {
		case <-release:
		default:
			close(release)
		}
	})

	cfg := GetInstance()
	cfg.setIPAddressExternal("")

	began := time.Now()
	ScheduleIPAddressExternalRefresh()
	if time.Since(began) > 200*time.Millisecond {
		t.Fatal("ScheduleIPAddressExternalRefresh blocked the caller")
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("lookup did not start")
	}
	close(release)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cfg.GetIPAddressExternal() == "203.0.113.9" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("external IP was not updated, got %q", cfg.GetIPAddressExternal())
}
