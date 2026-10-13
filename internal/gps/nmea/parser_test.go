package nmea

import (
	"errors"
	"fmt"
	"testing"
)

func TestParseGGA_SampleFix(t *testing.T) {
	sentence := "$GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,*47"
	msg, err := Parse(sentence)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !msg.IsValid() {
		t.Fatal("message should be valid")
	}
	assertCoords(t, msg, 48.11730, 11.51667)
}

func TestParseRMC_SampleFix(t *testing.T) {
	sentence := "$GPRMC,123519,A,4807.038,N,01131.000,E,022.4,084.4,230394,003.1,W*6A"
	msg, err := Parse(sentence)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !msg.IsValid() {
		t.Fatal("message should be valid")
	}
	assertCoords(t, msg, 48.11730, 11.51667)
}

func TestParse_DeviceCapture(t *testing.T) {
	voidRMC := "$GNRMC,132843.00,V,,,,,,,,,,N*6C"
	msg, err := Parse(voidRMC)
	if err != nil {
		t.Fatalf("void RMC parse failed: %v", err)
	}
	if msg.IsValid() {
		t.Fatal("void RMC should not be a fix")
	}

	voidGGA := "$GNGGA,132843.00,,,,,0,00,99.99,,,,,,*77"
	msg, err = Parse(voidGGA)
	if err != nil {
		t.Fatalf("void GGA parse failed: %v", err)
	}
	if msg.IsValid() {
		t.Fatal("quality 0 GGA should not be a fix")
	}

	fixRMC := "$GNRMC,132909.00,A,4059.43459,N,02903.44495,E,0.189,,131026,,,A*6E"
	msg, err = Parse(fixRMC)
	if err != nil {
		t.Fatalf("fix RMC parse failed: %v", err)
	}
	if !msg.IsValid() {
		t.Fatal("active RMC should be a fix")
	}
	assertCoords(t, msg, 40.99058, 29.05742)

	fixGGA := "$GNGGA,132909.00,4059.43459,N,02903.44495,E,1,06,3.38,38.8,M,37.5,M,,*79"
	msg, err = Parse(fixGGA)
	if err != nil {
		t.Fatalf("fix GGA parse failed: %v", err)
	}
	assertCoords(t, msg, 40.99058, 29.05742)
}

func TestParse_EquatorAndPrimeMeridianAreValid(t *testing.T) {
	sentence := withChecksum("GNRMC,123519.00,A,0000.00000,N,00000.00000,E,0.0,,010101,,,A")
	msg, err := Parse(sentence)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !msg.IsValid() {
		t.Fatal("equator and prime meridian fix should be valid")
	}
	assertCoords(t, msg, 0, 0)
}

func TestParse_SouthernAndWesternHemisphere(t *testing.T) {
	sentence := withChecksum("GNGGA,123519.00,3348.00000,S,11815.00000,W,2,08,1.0,10.0,M,0.0,M,,")
	msg, err := Parse(sentence)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if !msg.IsValid() {
		t.Fatal("differential fix should be valid")
	}
	assertCoords(t, msg, -33.80000, -118.25000)
}

func TestParse_ShortGGAStillCarriesPosition(t *testing.T) {
	sentence := withChecksum("GPGGA,123519,4807.038,N,01131.000,E,1")
	msg, err := Parse(sentence)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	assertCoords(t, msg, 48.11730, 11.51667)
}

func TestParse_RejectsUnusableFixes(t *testing.T) {
	cases := []string{
		withChecksum("GNRMC,123519.00,A,4059.43459,N,02903.44495,E,0.1,,131026,,,N"),
		withChecksum("GNGGA,123519.00,4059.43459,N,02903.44495,E,6,08,1.0,10.0,M,0.0,M,,"),
		withChecksum("GNRMC,123519.00,A,4061.00000,N,02903.44495,E,0.1,,131026,,,A"),
	}
	for i, sentence := range cases {
		msg, err := Parse(sentence)
		if err != nil {
			t.Fatalf("case %d parse failed: %v", i, err)
		}
		if msg.IsValid() {
			t.Fatalf("case %d should not be a fix", i)
		}
	}
}

func TestParse_OtherTalkerRMC(t *testing.T) {
	msg, err := Parse(withChecksum("GLRMC,123519.00,A,4807.038,N,01131.000,E,0.1,0.0,230394,003.1,W"))
	if err != nil {
		t.Fatalf("GLRMC parse failed: %v", err)
	}
	if !msg.IsValid() {
		t.Fatal("GLRMC fix should be valid")
	}
	assertCoords(t, msg, 48.11730, 11.51667)
}

func TestParse_SkipsUnrelatedSentencesAndBadChecksums(t *testing.T) {
	_, err := Parse(withChecksum("GNGSA,A,1,,,,,,,,,,,,,99.99,99.99,99.99"))
	if !errors.Is(err, ErrUnsupportedSentence) {
		t.Fatalf("expected unsupported sentence, got %v", err)
	}

	bad := "$GNRMC,132909.00,A,4059.43459,N,02903.44495,E,0.189,,131026,,,A*00"
	_, err = Parse(bad)
	if !errors.Is(err, ErrInvalidChecksum) {
		t.Fatalf("expected checksum error, got %v", err)
	}

	_, err = Parse("GNRMC,132909.00,A,4059.43459,N,02903.44495,E,0.189,,131026,,,A")
	if err == nil {
		t.Fatal("expected missing prefix to fail")
	}
}

func assertCoords(t *testing.T, msg Message, wantLat, wantLon float64) {
	t.Helper()
	got := fmt.Sprintf("%.5f,%.5f", msg.GetLatitude(), msg.GetLongitude())
	want := fmt.Sprintf("%.5f,%.5f", wantLat, wantLon)
	if got != want {
		t.Fatalf("coordinates %s, want %s", got, want)
	}
}

func withChecksum(body string) string {
	var sum byte
	for i := 0; i < len(body); i++ {
		sum ^= body[i]
	}
	return fmt.Sprintf("$%s*%02X", body, sum)
}
