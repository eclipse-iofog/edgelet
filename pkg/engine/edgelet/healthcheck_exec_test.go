//go:build linux

package edgelet

import (
	"bytes"
	"sync"
	"testing"
)

func TestLimitedBufferKeepsPrefixAndDrainsRest(t *testing.T) {
	b := &limitedBuffer{limit: 4}
	n, err := b.Write([]byte("abcdef"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 6 {
		t.Fatalf("write reported %d, want 6 (full drain)", n)
	}
	if got := b.String(); got != "abcd" {
		t.Fatalf("buffer %q, want abcd", got)
	}
	n, err = b.Write([]byte("zzzz"))
	if err != nil || n != 4 {
		t.Fatalf("second write n=%d err=%v", n, err)
	}
	if got := b.String(); got != "abcd" {
		t.Fatalf("buffer grew past cap: %q", got)
	}
}

func TestLimitedBufferConcurrentWrites(t *testing.T) {
	b := &limitedBuffer{limit: 8}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = b.Write(bytes.Repeat([]byte("x"), 16))
		}()
	}
	wg.Wait()
	if got := len(b.String()); got != 8 {
		t.Fatalf("len %d, want 8", got)
	}
}
