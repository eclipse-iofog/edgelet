package control

import (
	"os"
	"path/filepath"
	"testing"
)

func TestChooseResolvConfSkipsStub(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "stub.conf")
	upstream := filepath.Join(dir, "upstream.conf")
	writeResolv(t, host, "nameserver 127.0.0.53\n")
	writeResolv(t, upstream, "nameserver 192.0.2.53\n")
	if got := chooseResolvConf("", host, upstream); got != upstream {
		t.Fatalf("resolv %s", got)
	}
	if got := chooseResolvConf(host, host, upstream); got != host {
		t.Fatalf("configured resolv %s", got)
	}
}

func TestChooseResolvConfKeepsRealNameserver(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "host.conf")
	upstream := filepath.Join(dir, "upstream.conf")
	writeResolv(t, host, "nameserver 192.0.2.1\n")
	writeResolv(t, upstream, "nameserver 192.0.2.53\n")
	if got := chooseResolvConf("", host, upstream); got != host {
		t.Fatalf("resolv %s", got)
	}
}

func TestChooseResolvConfKeepsStubWhenUpstreamIsLoopback(t *testing.T) {
	dir := t.TempDir()
	host := filepath.Join(dir, "stub.conf")
	upstream := filepath.Join(dir, "also-stub.conf")
	writeResolv(t, host, "nameserver 127.0.0.53\n")
	writeResolv(t, upstream, "nameserver 127.0.0.1\n")
	if got := chooseResolvConf("", host, upstream); got != host {
		t.Fatalf("resolv %s", got)
	}
}

func writeResolv(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
