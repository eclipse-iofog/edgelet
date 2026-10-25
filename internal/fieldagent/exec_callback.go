package fieldagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	execCallbackModuleName = "ExecSessionCallback"
	execTimeout            = ControllerSessionMaxDuration
)

// ExecSessionCallback handles Docker exec session I/O and bridges it to WebSocket
type ExecSessionCallback struct {
	microserviceUUID string
	execID           string
	webSocketHandler *ExecSessionWebSocketHandler

	// I/O streams
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	stderr       io.ReadCloser
	stdinReader  io.ReadCloser  // For ProcessManager to read from
	stdoutWriter io.WriteCloser // For ProcessManager to write to
	stderrWriter io.WriteCloser // For ProcessManager to write to

	// State management
	isRunning    atomic.Bool
	stdoutClosed atomic.Bool
	stderrClosed atomic.Bool
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup

	// Handlers
	onInputHandler  func([]byte)
	onOutputHandler func([]byte)
	onErrorHandler  func([]byte)
	onCloseHandler  func()

	// Timeout
	timeoutTimer *time.Timer
}

// NewExecSessionCallback creates a new ExecSessionCallback
func NewExecSessionCallback(microserviceUUID, execID string) *ExecSessionCallback {
	ctx, cancel := context.WithCancel(context.Background())

	callback := &ExecSessionCallback{
		microserviceUUID: microserviceUUID,
		execID:           execID,
		webSocketHandler: GetExecSessionWebSocketHandler(execID, microserviceUUID),
		ctx:              ctx,
		cancel:           cancel,
	}

	callback.isRunning.Store(true)
	callback.stdoutClosed.Store(false)
	callback.stderrClosed.Store(false)

	// Create pipes for I/O
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	stderrReader, stderrWriter := io.Pipe()

	callback.stdin = stdinWriter
	callback.stdout = stdoutReader
	callback.stderr = stderrReader

	// Start goroutines to read from stdout and stderr
	callback.wg.Add(2)
	go callback.readStdout()
	go callback.readStderr()

	// Store writers for ProcessManager to use
	callback.stdoutWriter = stdoutWriter
	callback.stderrWriter = stderrWriter
	callback.stdinReader = stdinReader

	// Schedule timeout
	callback.scheduleTimeout()

	logging.LogDebug(execCallbackModuleName, fmt.Sprintf("Created ExecSessionCallback: microserviceUUID=%s, execID=%s", microserviceUUID, execID))
	return callback
}

// SetOnInputHandler sets the handler for input data
func (c *ExecSessionCallback) SetOnInputHandler(handler func([]byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onInputHandler = handler
}

// SetOnOutputHandler sets the handler for output data
func (c *ExecSessionCallback) SetOnOutputHandler(handler func([]byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onOutputHandler = handler
}

// SetOnErrorHandler sets the handler for error data
func (c *ExecSessionCallback) SetOnErrorHandler(handler func([]byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onErrorHandler = handler
}

// SetOnCloseHandler sets the handler for close events
func (c *ExecSessionCallback) SetOnCloseHandler(handler func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onCloseHandler = handler
}

// GetStdinReader returns the stdin reader for ProcessManager
func (c *ExecSessionCallback) GetStdinReader() io.Reader {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stdinReader
}

// GetStdoutWriter returns the stdout writer for ProcessManager
func (c *ExecSessionCallback) GetStdoutWriter() io.Writer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stdoutWriter
}

// GetStderrWriter returns the stderr writer for ProcessManager
func (c *ExecSessionCallback) GetStderrWriter() io.Writer {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stderrWriter
}

// readStdout reads from stdout and forwards to WebSocket
func (c *ExecSessionCallback) readStdout() {
	defer c.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(execCallbackModuleName, "Panic recovered", fmt.Errorf("%v", r))
		}
	}()
	defer c.stdoutClosed.Store(true)

	buffer := make([]byte, 1024)
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			if !c.isRunning.Load() {
				return
			}

			n, err := c.stdout.Read(buffer)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buffer[:n])
				c.forwardToWebSocket(ExecTypeStdout, data)
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) && err.Error() != "io: read/write on closed pipe" {
					logging.LogError(execCallbackModuleName, "Error reading from stdout", err)
				}
				return
			}
		}
	}
}

// readStderr reads from stderr and forwards to WebSocket
func (c *ExecSessionCallback) readStderr() {
	defer c.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(execCallbackModuleName, "Panic recovered", fmt.Errorf("%v", r))
		}
	}()
	defer c.stderrClosed.Store(true)

	buffer := make([]byte, 1024)
	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			if !c.isRunning.Load() {
				return
			}

			n, err := c.stderr.Read(buffer)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buffer[:n])
				c.forwardToWebSocket(ExecTypeStderr, data)
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) && err.Error() != "io: read/write on closed pipe" {
					logging.LogError(execCallbackModuleName, "Error reading from stderr", err)
				}
				return
			}
		}
	}
}

// forwardToWebSocket forwards data to the WebSocket handler (buffers when not connected/active).
func (c *ExecSessionCallback) forwardToWebSocket(msgType byte, data []byte) {
	if c.webSocketHandler == nil {
		return
	}
	if err := c.webSocketHandler.SendMessage(msgType, data); err != nil {
		logging.LogError(execCallbackModuleName, "Error forwarding to WebSocket", err)
	}

	// Also call handlers if set
	c.mu.RLock()
	if msgType == ExecTypeStdout && c.onOutputHandler != nil {
		c.onOutputHandler(data)
	} else if msgType == ExecTypeStderr && c.onErrorHandler != nil {
		c.onErrorHandler(data)
	}
	c.mu.RUnlock()
}

// WriteInput writes input data to stdin
func (c *ExecSessionCallback) WriteInput(data []byte) error {
	if !c.isRunning.Load() {
		return errors.New("session is not running")
	}

	c.mu.RLock()
	stdin := c.stdin
	c.mu.RUnlock()

	if stdin == nil {
		return errors.New("stdin is not available")
	}

	_, err := stdin.Write(data)
	if err != nil {
		logging.LogError(execCallbackModuleName, "Error writing to stdin", err)
		return err
	}

	// Call input handler if set
	c.mu.RLock()
	if c.onInputHandler != nil {
		c.onInputHandler(data)
	}
	c.mu.RUnlock()

	return nil
}

// OnActivation is called when the exec session is activated
func (c *ExecSessionCallback) OnActivation() {
	logging.LogInfo(execCallbackModuleName, "Exec session activated")
	// The WebSocket handler will handle activation separately
}

// OnComplete is called when the exec session completes.
// Stdout is drained before the close frame so the client still receives the
// shell's final output.
func (c *ExecSessionCallback) OnComplete() {
	logging.LogInfo(execCallbackModuleName, "Exec session completed")
	c.mu.RLock()
	onClose := c.onCloseHandler
	c.mu.RUnlock()

	if onClose != nil {
		onClose()
	}
	c.Close()
	c.notifyShellExit()
}

// OnError is called when the exec session encounters an error
func (c *ExecSessionCallback) OnError(err error) {
	logging.LogError(execCallbackModuleName, "Exec session error", err)
	c.Close()
	c.notifyShellExit()
}

// notifyShellExit tells the controller the PTY process has ended so the client
// can restore its local terminal. Bytes are not written to the shell.
func (c *ExecSessionCallback) notifyShellExit() {
	c.mu.RLock()
	handler := c.webSocketHandler
	c.mu.RUnlock()
	if handler == nil || !handler.IsConnected() || !handler.IsActive() {
		return
	}
	if err := handler.SendMessage(ExecTypeClose, []byte{}); err != nil {
		logging.LogWarn(execCallbackModuleName, fmt.Sprintf("Failed to notify exec session close: %v", err))
	}
}

// Close closes the exec session callback
func (c *ExecSessionCallback) Close() {
	if !c.isRunning.CompareAndSwap(true, false) {
		return
	}

	c.cancel()

	// Cancel timeout
	if c.timeoutTimer != nil {
		c.timeoutTimer.Stop()
	}

	// Close pipe WRITERS first so readStdout/readStderr get EOF and return.
	// When StartExecSession fails (e.g. pendingExec race), no process writes to the pipes,
	// so readStdout blocks on Read() until the write end is closed.
	c.mu.Lock()
	if c.stdoutWriter != nil {
		if err := c.stdoutWriter.Close(); err != nil {
			logging.LogWarn(execCallbackModuleName, fmt.Sprintf("Failed to close stdoutWriter: %v", err))
		}
		c.stdoutWriter = nil
	}
	if c.stderrWriter != nil {
		if err := c.stderrWriter.Close(); err != nil {
			logging.LogWarn(execCallbackModuleName, fmt.Sprintf("Failed to close stderrWriter: %v", err))
		}
		c.stderrWriter = nil
	}
	if c.stdin != nil {
		if err := c.stdin.Close(); err != nil {
			logging.LogWarn(execCallbackModuleName, fmt.Sprintf("Failed to close stdin: %v", err))
		}
	}
	if c.stdout != nil {
		if err := c.stdout.Close(); err != nil {
			logging.LogWarn(execCallbackModuleName, fmt.Sprintf("Failed to close stdout: %v", err))
		}
	}
	if c.stderr != nil {
		if err := c.stderr.Close(); err != nil {
			logging.LogWarn(execCallbackModuleName, fmt.Sprintf("Failed to close stderr: %v", err))
		}
	}
	if c.stdinReader != nil {
		if err := c.stdinReader.Close(); err != nil {
			logging.LogWarn(execCallbackModuleName, fmt.Sprintf("Failed to close stdinReader: %v", err))
		}
	}
	c.mu.Unlock()

	// Wait for goroutines to finish
	c.wg.Wait()

	logging.LogInfo(execCallbackModuleName, fmt.Sprintf("ExecSessionCallback closed: microserviceUUID=%s, execID=%s", c.microserviceUUID, c.execID))
}

// IsRunning returns whether the callback is running
func (c *ExecSessionCallback) IsRunning() bool {
	return c.isRunning.Load()
}

// scheduleTimeout schedules a timeout for the exec session
func (c *ExecSessionCallback) scheduleTimeout() {
	if c.timeoutTimer != nil {
		c.timeoutTimer.Stop()
	}
	c.timeoutTimer = time.AfterFunc(execTimeout, func() {
		logging.LogInfo(execCallbackModuleName, "Exec session timeout reached")
		c.Close()
	})
}

// GetStdin returns the stdin writer
func (c *ExecSessionCallback) GetStdin() io.WriteCloser {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.stdin
}

// SetExecID sets the exec session ID
func (c *ExecSessionCallback) SetExecID(execID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.execID = execID
}

// GetExecID returns the exec session ID
func (c *ExecSessionCallback) GetExecID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.execID
}
