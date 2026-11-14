package datastore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/k3s-io/kine/pkg/drivers/sqlite"
	kinetls "github.com/k3s-io/kine/pkg/tls"
)

func TestNATSCredsDropUserinfo(t *testing.T) {
	const creds = "/var/lib/edgelet-kube/nats.creds"
	dsn, tlsCfg, err := prepare(Config{
		Endpoint:  "nats://user:secret@nats.example:4222?bucket=kube&replicas=3&revHistory=20",
		NATSCreds: creds,
		CertFile:  "/cert.pem",
		KeyFile:   "/key.pem",
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "nats" {
		t.Fatalf("scheme %s", u.Scheme)
	}
	if u.User != nil {
		t.Fatalf("userinfo %s", u.User)
	}
	if strings.Contains(dsn, "secret") || strings.Contains(dsn, "user:") {
		t.Fatalf("userinfo remains in %s", dsn)
	}
	q := u.Query()
	if q.Get("credsFile") != creds {
		t.Fatalf("credsFile %q", q.Get("credsFile"))
	}
	if !q.Has("noEmbed") {
		t.Fatalf("missing noEmbed in %s", dsn)
	}
	if q.Get("bucket") != "kube" || q.Get("replicas") != "3" || q.Get("revHistory") != "20" {
		t.Fatalf("query %s", q.Encode())
	}
	if tlsCfg.CertFile != "/cert.pem" || tlsCfg.KeyFile != "/key.pem" {
		t.Fatalf("client certificate %+v", tlsCfg)
	}
	if strings.Contains(dsn, "cert.pem") || strings.Contains(dsn, "key.pem") {
		t.Fatalf("certificate leaked into %s", dsn)
	}
}

func TestNATSCAKeepsScheme(t *testing.T) {
	const ca = "/var/lib/edgelet-kube/nats-ca.pem"
	in := "nats://user:secret@nats.example:4222?bucket=kube&replicas=2&revHistory=8"
	dsn, tlsCfg, err := prepare(Config{Endpoint: in, CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "nats" {
		t.Fatalf("scheme %s", u.Scheme)
	}
	if strings.HasPrefix(dsn, "tls:") {
		t.Fatalf("scheme rewritten %s", dsn)
	}
	if !u.Query().Has("noEmbed") {
		t.Fatalf("missing noEmbed in %s", dsn)
	}
	if u.User == nil || u.User.Username() != "user" {
		t.Fatalf("userinfo %v", u.User)
	}
	q := u.Query()
	if q.Get("bucket") != "kube" || q.Get("replicas") != "2" || q.Get("revHistory") != "8" {
		t.Fatalf("query %s", q.Encode())
	}
	if q.Get("credsFile") != "" {
		t.Fatalf("credsFile %q", q.Get("credsFile"))
	}
	if tlsCfg.CAFile != ca {
		t.Fatalf("CA %q", tlsCfg.CAFile)
	}
	if strings.Contains(dsn, ca) {
		t.Fatalf("CA leaked into %s", dsn)
	}

	bare, _, err := prepare(Config{Endpoint: "nats://", CAFile: ca})
	if err != nil {
		t.Fatal(err)
	}
	u, err = url.Parse(bare)
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "nats" || u.Host != "" || !u.Query().Has("noEmbed") {
		t.Fatalf("bare endpoint %s", bare)
	}
}

func TestNATSClientCertOptional(t *testing.T) {
	in := "nats://nats.example:4222?bucket=kube&replicas=3&revHistory=4"
	dsn, tlsCfg, err := prepare(Config{
		Endpoint: in,
		CertFile: "/cert.pem",
		KeyFile:  "/key.pem",
	})
	if err != nil {
		t.Fatal(err)
	}
	if dsn != in {
		t.Fatalf("dsn %s", dsn)
	}
	if tlsCfg != (kinetls.Config{CertFile: "/cert.pem", KeyFile: "/key.pem"}) {
		t.Fatalf("tls %+v", tlsCfg)
	}

	dsn, tlsCfg, err = prepare(Config{Endpoint: in})
	if err != nil {
		t.Fatal(err)
	}
	if dsn != in {
		t.Fatalf("dsn %s", dsn)
	}
	if tlsCfg != (kinetls.Config{}) {
		t.Fatalf("tls %+v", tlsCfg)
	}
}

func TestSQLAndEtcdPassThrough(t *testing.T) {
	endpoints := []string{
		"mysql://user:secret@db.example/kube",
		"postgres://user:secret@db.example/kube",
		"http://etcd.example:2379",
		"https://etcd.example:2379",
		"sqlite:///tmp/custom.db?_journal_mode=WAL",
	}
	for _, endpoint := range endpoints {
		got, tlsCfg, err := prepare(Config{
			Endpoint:  endpoint,
			CAFile:    "/ca.pem",
			CertFile:  "/cert.pem",
			KeyFile:   "/key.pem",
			NATSCreds: "/nats.creds",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != endpoint {
			t.Fatalf("dsn %s", got)
		}
		if strings.Contains(got, "noEmbed") || strings.Contains(got, "credsFile") {
			t.Fatalf("nats parameters in %s", got)
		}
		if tlsCfg != (kinetls.Config{CAFile: "/ca.pem", CertFile: "/cert.pem", KeyFile: "/key.pem"}) {
			t.Fatalf("tls %+v", tlsCfg)
		}
	}
}

func TestAbsentFlagsDoNotQueryBus(t *testing.T) {
	queried := false
	prev := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(context.Context, string, string) (net.Conn, error) {
			queried = true
			return nil, errors.New("network query")
		},
	}
	t.Cleanup(func() { net.DefaultResolver = prev })

	endpoints := []string{
		"",
		"postgres://db.example/kube",
		"mysql://db.example/kube",
		"http://etcd.example:2379",
		"https://etcd.example:2379",
		"sqlite:///tmp/state.db",
		"nats://nats.example:4222?bucket=kine&replicas=1&revHistory=10",
		"nats://",
	}
	for _, endpoint := range endpoints {
		got, tlsCfg, err := prepare(Config{Endpoint: endpoint})
		if err != nil {
			t.Fatal(err)
		}
		if got != endpoint {
			t.Fatalf("endpoint %q became %q", endpoint, got)
		}
		if tlsCfg != (kinetls.Config{}) {
			t.Fatalf("tls %+v", tlsCfg)
		}
	}
	if queried {
		t.Fatal("queried a network name")
	}
}

func TestNATSCredsOnSeveralHosts(t *testing.T) {
	dsn, _, err := prepare(Config{
		Endpoint:  "nats://user:secret@a.example:4222?bucket=kube,nats://user:secret@b.example:4222",
		NATSCreds: "/nats.creds",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dsn, "secret") || strings.Contains(dsn, "user:") {
		t.Fatalf("userinfo remains in %s", dsn)
	}
	parts := strings.Split(dsn, ",")
	if len(parts) != 2 {
		t.Fatalf("dsn %s", dsn)
	}
	first, err := url.Parse(parts[0])
	if err != nil {
		t.Fatal(err)
	}
	if first.Host != "a.example:4222" || first.Query().Get("credsFile") != "/nats.creds" || !first.Query().Has("noEmbed") {
		t.Fatalf("first %s", parts[0])
	}
	if first.Query().Get("bucket") != "kube" {
		t.Fatalf("bucket %s", first.RawQuery)
	}
	second, err := url.Parse(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	if second.Host != "b.example:4222" || second.User != nil {
		t.Fatalf("second %s", parts[1])
	}
}

func TestSQLiteEndpointLeaderElectionOff(t *testing.T) {
	dir := filepath.Join("/tmp", fmt.Sprintf("edgelet-kube-sqlite-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	db := filepath.Join(dir, "custom.db")
	dsn := "sqlite://" + db + "?" + sqlite.DefaultParams

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store, err := Open(ctx, Config{DataDir: dir, Endpoint: dsn})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)

	if store.LeaderElect {
		t.Fatal("leader election is on")
	}
	if store.Path != db {
		t.Fatalf("path %s", store.Path)
	}
	if journalMode(t, db) != 2 {
		t.Fatal("journal mode")
	}
	if fileExists(filepath.Join(dir, "db", "state.db")) {
		t.Fatal("created state.db")
	}
}

func TestExternalEtcdLeaderElectionOn(t *testing.T) {
	dir := filepath.Join("/tmp", fmt.Sprintf("edgelet-kube-etcd-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for _, endpoint := range []string{"http://127.0.0.1:2379", "https://127.0.0.1:2379"} {
		store, err := Open(ctx, Config{DataDir: dir, Endpoint: endpoint, CAFile: "/ca.pem"})
		if err != nil {
			t.Fatal(err)
		}
		if !store.LeaderElect {
			t.Fatalf("%s leader election is off", endpoint)
		}
		if store.Endpoint != endpoint {
			t.Fatalf("%s endpoint %s", endpoint, store.Endpoint)
		}
		store.Close()
	}
	if fileExists(filepath.Join(dir, "db", "state.db")) {
		t.Fatal("created state.db")
	}
}

func TestEmptyEndpointIgnoresNATSFlags(t *testing.T) {
	dir := filepath.Join("/tmp", fmt.Sprintf("edgelet-kube-empty-nats-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store, err := Open(ctx, Config{
		DataDir:   dir,
		CAFile:    "/ca.pem",
		CertFile:  "/cert.pem",
		KeyFile:   "/key.pem",
		NATSCreds: "/nats.creds",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(store.Close)
	if store.LeaderElect {
		t.Fatal("leader election is on")
	}
	if !strings.HasPrefix(store.Endpoint, "unix://") || !strings.Contains(store.Endpoint, "/kine.sock") {
		t.Fatalf("endpoint %s", store.Endpoint)
	}
	if store.Path != filepath.Join(dir, "db", "state.db") {
		t.Fatalf("path %s", store.Path)
	}
}
