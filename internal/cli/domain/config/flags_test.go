package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestFlagSet_CollectLongAndShortFlags(t *testing.T) {
	cmd := &cobra.Command{Use: "config"}
	fs := NewFlagSet()
	fs.Register(cmd)
	if err := cmd.ParseFlags([]string{
		"--controller-url", "http://localhost:51121/api/v3",
		"--cf", "10",
		"--sf", "10",
	}); err != nil {
		t.Fatalf("ParseFlags: %v", err)
	}

	setMap, err := fs.Collect(cmd.Flags())
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if got := setMap["controllerUrl"]; got != "http://localhost:51121/api/v3" {
		t.Fatalf("controllerUrl=%v", got)
	}
	if got := setMap["changeFrequencySeconds"]; got != 10 {
		t.Fatalf("changeFrequencySeconds=%v", got)
	}
	if got := setMap["statusFrequencySeconds"]; got != 10 {
		t.Fatalf("statusFrequencySeconds=%v", got)
	}
}

func TestFlagSet_ShortAliasEquivalentToLong(t *testing.T) {
	longCmd := &cobra.Command{Use: "config"}
	shortCmd := &cobra.Command{Use: "config"}
	longFS := NewFlagSet()
	shortFS := NewFlagSet()
	longFS.Register(longCmd)
	shortFS.Register(shortCmd)

	if err := longCmd.ParseFlags([]string{"--change-frequency-seconds", "15"}); err != nil {
		t.Fatalf("long ParseFlags: %v", err)
	}
	if err := shortCmd.ParseFlags([]string{"--cf", "15"}); err != nil {
		t.Fatalf("short ParseFlags: %v", err)
	}

	longMap, err := longFS.Collect(longCmd.Flags())
	if err != nil {
		t.Fatalf("long Collect: %v", err)
	}
	shortMap, err := shortFS.Collect(shortCmd.Flags())
	if err != nil {
		t.Fatalf("short Collect: %v", err)
	}
	if longMap["changeFrequencySeconds"] != shortMap["changeFrequencySeconds"] {
		t.Fatalf("long=%v short=%v", longMap, shortMap)
	}
}

func TestFlagSetOmitsDeviceScanFrequency(t *testing.T) {
	cmd := &cobra.Command{Use: "config"}
	fs := NewFlagSet()
	fs.Register(cmd)
	if cmd.Flags().Lookup("device-scan-frequency") != nil {
		t.Fatal("expected --device-scan-frequency to be removed")
	}
	if cmd.Flags().Lookup("sd") != nil {
		t.Fatal("expected --sd to be removed")
	}
	if _, ok := configKeyRules["deviceScanFrequency"]; ok {
		t.Fatal("configKeyRules must not include deviceScanFrequency")
	}
}

func TestFlagSet_RequiresAtLeastOneFlag(t *testing.T) {
	cmd := &cobra.Command{Use: "config"}
	fs := NewFlagSet()
	fs.Register(cmd)
	_, err := fs.Collect(cmd.Flags())
	if err == nil || !strings.Contains(err.Error(), "at least one config flag is required") {
		t.Fatalf("expected required flag error, got %v", err)
	}
}

func TestValidateConfigValue_RejectsInvalidBool(t *testing.T) {
	rule := configKeyRules["secureMode"]
	_, err := validateAndNormalizeConfigValue(rule, "maybe")
	if err == nil {
		t.Fatal("expected error for invalid bool")
	}
	if !strings.Contains(err.Error(), "must be one of true|false|on|off|1|0") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateConfigValue_ControllerCertRequiresReadablePEMPath(t *testing.T) {
	tempDir := t.TempDir()
	validCertPath := filepath.Join(tempDir, "controller.crt")
	if err := os.WriteFile(validCertPath, []byte(generateTestPEMCertificate(t)), 0o600); err != nil {
		t.Fatalf("failed to write test cert: %v", err)
	}

	rule := configKeyRules["controllerCert"]
	value, err := validateAndNormalizeConfigValue(rule, validCertPath)
	if err != nil {
		t.Fatalf("expected valid controller cert path, got error: %v", err)
	}
	if value != validCertPath {
		t.Fatalf("expected controllerCert path %q, got %v", validCertPath, value)
	}

	_, err = validateAndNormalizeConfigValue(rule, filepath.Join(tempDir, "missing.crt"))
	if err == nil || !strings.Contains(err.Error(), "readable PEM certificate file path") {
		t.Fatalf("expected missing file validation error, got: %v", err)
	}
}

func TestCamelToKebab(t *testing.T) {
	if got := camelToKebab("changeFrequencySeconds"); got != "change-frequency-seconds" {
		t.Fatalf("unexpected kebab: %q", got)
	}
	if got := camelToKebab("controllerUrl"); got != "controller-url" {
		t.Fatalf("unexpected kebab: %q", got)
	}
}

func TestLongFlagName_ExplicitGiBAndMiB(t *testing.T) {
	if got := longFlagName("diskLimitGiB"); got != "disk-limit-gib" {
		t.Fatalf("diskLimitGiB=%q", got)
	}
	if got := longFlagName("memoryLimitMiB"); got != "memory-limit-mib" {
		t.Fatalf("memoryLimitMiB=%q", got)
	}
	if got := longFlagName("logLimitGiB"); got != "log-limit-gib" {
		t.Fatalf("logLimitGiB=%q", got)
	}
}

func TestCommandLong_IsShortIntro(t *testing.T) {
	long := CommandLong()
	if strings.Contains(long, "Settings:") {
		t.Fatalf("expected short intro without settings table: %q", long)
	}
	if !strings.Contains(long, "config cert") {
		t.Fatalf("expected cert mention: %q", long)
	}
}

func generateTestPEMCertificate(t *testing.T) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "cli-test-cert",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}
