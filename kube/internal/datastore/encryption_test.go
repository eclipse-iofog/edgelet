package datastore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func shortDir(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join("/tmp", fmt.Sprintf("ek-%d-%s", os.Getpid(), name))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestEncryptionConfigPath(t *testing.T) {
	if got := EncryptionConfigPath(""); got != "/var/lib/edgelet-kube/server/cred/encryption-config.json" {
		t.Fatal(got)
	}
}

func TestEncryptionOffWritesNoFile(t *testing.T) {
	dir := shortDir(t, "off")
	got, err := PrepareEncryption(dir, false)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderConfig != "" {
		t.Fatalf("provider config %s", got.ProviderConfig)
	}
	if fileExists(EncryptionConfigPath(dir)) {
		t.Fatal("wrote encryption config")
	}
	if _, err := os.Stat(filepath.Join(dir, "server")); !os.IsNotExist(err) {
		t.Fatal("created the cred directory")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store, err := Open(ctx, Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if store.EncryptionConfig != "" {
		t.Fatalf("provider config %s", store.EncryptionConfig)
	}
	if store.LeaderElect {
		t.Fatal("leader election is on")
	}
	if !fileExists(StateDB(dir)) {
		t.Fatal("state.db was not created")
	}
	if fileExists(EncryptionConfigPath(dir)) {
		t.Fatal("wrote encryption config")
	}
}

func TestEncryptionFirstBoot(t *testing.T) {
	dir := shortDir(t, "boot")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store, err := Open(ctx, Config{DataDir: dir, SecretsEncryption: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	path := EncryptionConfigPath(dir)
	if store.EncryptionConfig != path {
		t.Fatalf("provider config %s", store.EncryptionConfig)
	}
	if store.LeaderElect {
		t.Fatal("leader election is on")
	}
	if store.Path != StateDB(dir) {
		t.Fatalf("path %s", store.Path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	assertAESCBC(t, body)

	again, err := PrepareEncryption(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if again.ProviderConfig != path {
		t.Fatalf("provider config %s", again.ProviderConfig)
	}
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, kept) {
		t.Fatal("encryption config changed")
	}

	store.Close()
	reopened, err := Open(ctx, Config{DataDir: dir, SecretsEncryption: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reopened.Close)
	if reopened.EncryptionConfig != path {
		t.Fatalf("provider config %s", reopened.EncryptionConfig)
	}
	kept, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, kept) {
		t.Fatal("encryption config changed")
	}
}

func TestEncryptionOnExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	db := StateDB(dir)
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := PrepareEncryption(dir, true)
	if err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatal(err)
	}
	if fileExists(EncryptionConfigPath(dir)) {
		t.Fatal("wrote encryption config")
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := Open(ctx, Config{DataDir: dir, SecretsEncryption: true}); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatal(err)
	}
	if fileExists(EncryptionConfigPath(dir)) {
		t.Fatal("wrote encryption config")
	}
}

func TestEncryptionOffKeyPresent(t *testing.T) {
	dir := t.TempDir()
	path := EncryptionConfigPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const body = "existing-key\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := PrepareEncryption(dir, false)
	if err == nil || !strings.Contains(err.Error(), "is off") {
		t.Fatal(err)
	}
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != body {
		t.Fatalf("key file %q", kept)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	if _, err := Open(ctx, Config{DataDir: dir}); err == nil || !strings.Contains(err.Error(), "is off") {
		t.Fatal(err)
	}
	if fileExists(StateDB(dir)) {
		t.Fatal("created state.db")
	}
}

func TestEncryptionKeepsExistingKey(t *testing.T) {
	dir := t.TempDir()
	path := EncryptionConfigPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	const body = "existing-key\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	db := StateDB(dir)
	if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(db, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := PrepareEncryption(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProviderConfig != path {
		t.Fatalf("provider config %s", got.ProviderConfig)
	}
	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != body {
		t.Fatalf("key file %q", kept)
	}
}

func assertAESCBC(t *testing.T, body []byte) {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["kind"] != "EncryptionConfiguration" || doc["apiVersion"] != "apiserver.config.k8s.io/v1" {
		t.Fatalf("document %#v", doc)
	}
	resources := valueAs[[]any](t, doc["resources"])
	if len(resources) != 1 {
		t.Fatalf("resources %#v", doc["resources"])
	}
	resource := valueAs[map[string]any](t, resources[0])
	names := valueAs[[]any](t, resource["resources"])
	if len(names) != 1 || names[0] != "secrets" {
		t.Fatalf("resources %#v", resource["resources"])
	}
	providers := valueAs[[]any](t, resource["providers"])
	if len(providers) != 2 {
		t.Fatalf("providers %#v", resource["providers"])
	}
	first := valueAs[map[string]any](t, providers[0])
	aescbc := valueAs[map[string]any](t, first["aescbc"])
	keys := valueAs[[]any](t, aescbc["keys"])
	if len(keys) != 1 {
		t.Fatalf("aescbc %#v", first["aescbc"])
	}
	key := valueAs[map[string]any](t, keys[0])
	if key["name"] != "aescbckey" {
		t.Fatalf("key %#v", key)
	}
	secret := valueAs[string](t, key["secret"])
	raw, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 32 {
		t.Fatalf("key length %d", len(raw))
	}
	second := valueAs[map[string]any](t, providers[1])
	if _, ok := second["identity"]; !ok {
		t.Fatalf("providers %#v", providers)
	}
	if bytes.Contains(body, []byte("secretbox")) {
		t.Fatalf("document %s", body)
	}
}

func valueAs[T any](t *testing.T, v any) T {
	t.Helper()
	got, ok := v.(T)
	if !ok {
		var zero T
		t.Fatalf("got %T, want %T", v, zero)
	}
	return got
}
