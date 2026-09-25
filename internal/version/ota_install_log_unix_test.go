//go:build !windows

package version

import (
	"bytes"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"golang.org/x/sys/unix"
)

func TestDetachedInstallExitIsLogged(t *testing.T) {
	prev := logging.GetInstance().GetLevel()
	logging.GetInstance().SetLevel("info")
	t.Cleanup(func() { logging.GetInstance().SetLevel(prev.String()) })

	out := captureStderr(t, func() {
		logDetachedInstallExit(errors.New("exit status 3"))
	})
	if !bytes.Contains([]byte(out), []byte("install.sh exited with an error")) {
		t.Fatalf("log output=%q", out)
	}
	if !bytes.Contains([]byte(out), []byte("exit status 3")) {
		t.Fatalf("log output=%q", out)
	}

	out = captureStderr(t, func() {
		logDetachedInstallExit(nil)
	})
	if bytes.Contains([]byte(out), []byte("install.sh exited with an error")) {
		t.Fatalf("zero exit was logged: %q", out)
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	old, err := unix.Dup(int(os.Stderr.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(old)
	if err := unix.Dup2(int(w.Fd()), int(os.Stderr.Fd())); err != nil {
		t.Fatal(err)
	}

	fn()

	if err := unix.Dup2(old, int(os.Stderr.Fd())); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}
