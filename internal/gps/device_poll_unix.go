//go:build unix

package gps

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// readLineWithTimeout reads a line from reader with timeout (unix poll).
func (d *DeviceHandler) readLineWithTimeout(file *os.File, reader *bufio.Reader, timeout time.Duration) (string, error) {
	if file == nil {
		return "", errors.New("nil device file")
	}

	pollFds := []unix.PollFd{{
		Fd:     int32(file.Fd()), // #nosec G115 -- serial device FD from os.File fits int32 on supported platforms
		Events: unix.POLLIN | unix.POLLPRI,
	}}
	pollTimeoutMs := int(timeout.Milliseconds())
	if pollTimeoutMs < 0 {
		pollTimeoutMs = -1
	}

	n, err := unix.Poll(pollFds, pollTimeoutMs)
	if err != nil {
		return "", fmt.Errorf("poll failed: %w", err)
	}
	if n == 0 {
		return "", errReadTimeout
	}
	if pollFds[0].Revents&(unix.POLLERR|unix.POLLHUP|unix.POLLNVAL) != 0 {
		return "", errors.New("device poll error")
	}
	if pollFds[0].Revents&(unix.POLLIN|unix.POLLPRI) == 0 {
		return "", errors.New("device not readable")
	}

	if err := file.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", fmt.Errorf("set read deadline: %w", err)
	}
	defer func() {
		_ = file.SetReadDeadline(time.Time{})
	}()

	line, err := readBoundedLine(reader)
	if err != nil {
		if errors.Is(err, os.ErrDeadlineExceeded) {
			return "", errReadTimeout
		}
		return "", err
	}
	return line, nil
}

// nextLine returns the next NMEA line. Complete lines already in the reader
// buffer are taken before polling the device, so a scan sees the newest
// sentence in the backlog rather than stopping on the first buffered line.
func (d *DeviceHandler) nextLine(file *os.File, reader *bufio.Reader, timeout time.Duration) (string, error) {
	if line, ok, err := readBufferedLine(reader); ok || err != nil {
		return line, err
	}
	return d.readLineWithTimeout(file, reader, timeout)
}

func readBufferedLine(reader *bufio.Reader) (string, bool, error) {
	buffered := reader.Buffered()
	if buffered == 0 {
		return "", false, nil
	}
	peeked, err := reader.Peek(buffered)
	if err != nil {
		return "", false, err
	}
	if !bytes.Contains(peeked, []byte{'\n'}) {
		return "", false, nil
	}
	line, err := readBoundedLine(reader)
	if err != nil {
		return "", false, err
	}
	return line, true, nil
}

func readBoundedLine(reader *bufio.Reader) (string, error) {
	var buf []byte
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return "", err
		}
		if b == '\n' {
			return strings.TrimSpace(string(buf)), nil
		}
		if b == '\r' {
			continue
		}
		if len(buf) >= maxNMEALineLength {
			if err := discardUntilNewline(reader); err != nil {
				return "", err
			}
			return "", errOversizedLine
		}
		buf = append(buf, b)
	}
}

func discardUntilNewline(reader *bufio.Reader) error {
	for {
		b, err := reader.ReadByte()
		if err != nil {
			return err
		}
		if b == '\n' {
			return nil
		}
	}
}
