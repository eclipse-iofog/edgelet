//go:build unix

package gps

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
)

func TestReadLineWithTimeout_TimesOutWithoutData(t *testing.T) {
	readerEnd, writerEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	defer func() {
		_ = readerEnd.Close()
	}()
	defer func() {
		_ = writerEnd.Close()
	}()

	d := &DeviceHandler{}
	reader := bufio.NewReader(readerEnd)
	start := time.Now()
	_, err = d.readLineWithTimeout(readerEnd, reader, 50*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
	if time.Since(start) < 40*time.Millisecond {
		t.Fatal("read returned too quickly, expected timeout wait")
	}
}

func TestReadAndUpdateCoordinates_KeepsNewestFix(t *testing.T) {
	cfg := config.GetInstance()
	cfg.GPSCoordinates = "1.00000,2.00000"

	handler, writer := testDeviceHandler(t, 200*time.Millisecond)
	writeNMEA(t, writer,
		"$GNRMC,132843.00,V,,,,,,,,,,N*6C",
		"$GNRMC,132909.00,A,4059.43459,N,02903.44495,E,0.189,,131026,,,A*6E",
		"$GNRMC,132910.00,A,4059.42910,N,02903.43836,E,0.092,,131026,,,A*6E",
	)

	if err := handler.ReadAndUpdateCoordinates(); err != nil {
		t.Fatalf("read failed: %v", err)
	}
	if cfg.GPSCoordinates != "40.99048,29.05731" {
		t.Fatalf("coordinates %s, want newest fix 40.99048,29.05731", cfg.GPSCoordinates)
	}
}

func TestReadAndUpdateCoordinates_VoidAfterFixKeepsPrevious(t *testing.T) {
	cfg := config.GetInstance()
	cfg.GPSCoordinates = "40.99058,29.05742"

	handler, writer := testDeviceHandler(t, 200*time.Millisecond)
	writeNMEA(t, writer,
		"$GNRMC,132909.00,A,4059.43459,N,02903.44495,E,0.189,,131026,,,A*6E",
		"$GNRMC,132845.00,V,,,,,,,131026,,,N*6D",
	)

	err := handler.ReadAndUpdateCoordinates()
	if !errors.Is(err, errNoFix) {
		t.Fatalf("expected no fix, got %v", err)
	}
	if cfg.GPSCoordinates != "40.99058,29.05742" {
		t.Fatalf("coordinates changed to %s", cfg.GPSCoordinates)
	}
}

func TestReadAndUpdateCoordinates_SilentPortIsDeviceError(t *testing.T) {
	handler, _ := testDeviceHandler(t, 40*time.Millisecond)
	err := handler.ReadAndUpdateCoordinates()
	if err == nil || errors.Is(err, errNoFix) {
		t.Fatalf("expected device error, got %v", err)
	}
	if !errors.Is(err, errReadTimeout) {
		t.Fatalf("expected read timeout, got %v", err)
	}
}

func testDeviceHandler(t *testing.T, firstTimeout time.Duration) (*DeviceHandler, *os.File) {
	t.Helper()
	readerEnd, writerEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe: %v", err)
	}
	t.Cleanup(func() {
		_ = readerEnd.Close()
		_ = writerEnd.Close()
	})
	handler := &DeviceHandler{
		config:      config.GetInstance(),
		deviceFile:  readerEnd,
		reader:      bufio.NewReader(readerEnd),
		isRunning:   true,
		readTimeout: firstTimeout,
		idleTimeout: 30 * time.Millisecond,
	}
	return handler, writerEnd
}

func writeNMEA(t *testing.T, writer *os.File, lines ...string) {
	t.Helper()
	for _, line := range lines {
		if _, err := fmt.Fprintln(writer, line); err != nil {
			t.Fatalf("failed to write NMEA line: %v", err)
		}
	}
}
