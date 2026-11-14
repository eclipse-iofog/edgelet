package datastore

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sirupsen/logrus"
)

func TestMain(m *testing.M) {
	logrus.SetOutput(io.Discard)
	m.Run()
}

func TestEmptyEndpointOpensSQLite(t *testing.T) {
	dir := filepath.Join("/tmp", fmt.Sprintf("edgelet-kube-db-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	agentExisted := fileExists(agentDB)
	defaultExisted := fileExists(StateDB(""))

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store, err := Open(ctx, Config{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	if store.LeaderElect {
		t.Fatal("leader election is on")
	}
	if store.EncryptionConfig != "" {
		t.Fatalf("encryption config %s", store.EncryptionConfig)
	}
	if fileExists(EncryptionConfigPath(dir)) {
		t.Fatal("encryption config was written")
	}
	want := filepath.Join(dir, "db", "state.db")
	if store.Path != want {
		t.Fatalf("path %s", store.Path)
	}
	if !strings.Contains(store.Endpoint, "kine.sock") {
		t.Fatalf("kine endpoint %q", store.Endpoint)
	}
	info, err := os.Stat(want)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() || info.Size() == 0 {
		t.Fatalf("state.db size %d dir %v", info.Size(), info.IsDir())
	}
	if mode := journalMode(t, want); mode != 2 {
		t.Fatalf("journal mode %d", mode)
	}
	if fileExists(filepath.Join(dir, "edgelet.db")) || fileExists(filepath.Join(dir, "db", "edgelet.db")) {
		t.Fatal("agent database was created in the data dir")
	}
	if fileExists(filepath.Join(dir, "db", "etcd")) {
		t.Fatal("etcd directory was created")
	}
	if !agentExisted && fileExists(agentDB) {
		t.Fatal("agent database was created")
	}
	if !defaultExisted && fileExists(StateDB("")) {
		t.Fatal("default database was created for a temporary data dir")
	}
}

func TestDefaultDatabasePath(t *testing.T) {
	if got := StateDB(""); got != "/var/lib/edgelet-kube/db/state.db" {
		t.Fatal(got)
	}
}

func TestSetEndpointDoesNotOpenSQLite(t *testing.T) {
	dir := filepath.Join("/tmp", fmt.Sprintf("edgelet-kube-db-set-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := Open(ctx, Config{DataDir: dir, Endpoint: "foo://db.example/kube"})
	if err == nil {
		t.Fatal("opened a set endpoint")
	}
	if fileExists(filepath.Join(dir, "db", "state.db")) {
		t.Fatal("created state.db")
	}
}

func TestSQLiteEndpointRejectsAgentDB(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	_, err := Open(ctx, Config{Endpoint: "sqlite://" + agentDB})
	if err == nil {
		t.Fatal("opened the agent database")
	}
}

func journalMode(t *testing.T, path string) byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = f.Close()
	}()
	hdr := make([]byte, 20)
	if _, err := io.ReadFull(f, hdr); err != nil {
		t.Fatal(err)
	}
	if string(hdr[:16]) != "SQLite format 3\x00" {
		t.Fatalf("header %q", hdr[:16])
	}
	return hdr[18]
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
