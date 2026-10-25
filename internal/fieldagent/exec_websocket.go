package fieldagent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

const (
	execWebSocketModuleName       = "Exec Session WebSocket Handler"
	pingInterval                  = 30 * time.Second
	execPendingReadTimeout        = 120 * time.Second // matches Controller logPendingTimeoutMs
	handshakeTimeout              = 10 * time.Second
	execCloseReasonSessionStopped = "session stopped"
	maxFrameSize                  = 65536
	maxBufferSize                 = 1024 * 1024 // 1MB
	maxBufferedFrames             = 1000
	minExecCols                   = 2
	maxExecCols                   = 4096
	minExecRows                   = 1
	maxExecRows                   = 4096
	execResizeRetryAttempts       = 25
	execResizeRetryInterval       = 200 * time.Millisecond
)

var errExecResizeNotReady = errors.New("exec session is not ready to resize")

type resizeReq struct {
	cols uint32
	rows uint32
}

type execResizeControl struct {
	Type string `json:"type"`
	Cols uint32 `json:"cols"`
	Rows uint32 `json:"rows"`
}

// ExecMessageType constants
const (
	ExecTypeStdin      byte = 0
	ExecTypeStdout     byte = 1
	ExecTypeStderr     byte = 2
	ExecTypeControl    byte = 3
	ExecTypeClose      byte = 4
	ExecTypeActivation byte = 5
)

// ConnectionState represents the WebSocket connection state
type ConnectionState int

const (
	StateDisconnected ConnectionState = iota
	StateConnecting
	StateConnected
	StatePending // Connected but waiting for user
	StateActive  // Connected and paired with user
)

type execFrame struct {
	msgType byte
	data    []byte
}

// ExecSessionWebSocketHandler manages the WebSocket connection for exec sessions
type ExecSessionWebSocketHandler struct {
	controllerWsURL    string
	sessionID          string
	microserviceUUID   string
	conn               *websocket.Conn
	connMu             sync.RWMutex
	writeMu            sync.Mutex // Protects concurrent writes to websocket
	isConnected        atomic.Bool
	isActive           atomic.Bool
	state              atomic.Value // ConnectionState
	outputBuffer       chan execFrame
	bufferedSize       atomic.Int64
	bufferedFrames     atomic.Int32
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	pingTicker         *time.Ticker
	config             *config.Config
	jwtManager         *auth.JWTManager
	controllerTLS      *tls.Config
	execSessionManager *ExecSessionManager
	lifecycleMu        sync.Mutex
	resizeMu           sync.Mutex
	resizeCh           chan resizeReq
}

var (
	activeExecHandlers sync.Map // map[string]*ExecSessionWebSocketHandler keyed by sessionId
)

// GetExecSessionWebSocketHandler gets or creates an ExecSessionWebSocketHandler for a session.
func GetExecSessionWebSocketHandler(sessionID, microserviceUUID string) *ExecSessionWebSocketHandler {
	handler, _ := activeExecHandlers.LoadOrStore(sessionID, newExecSessionWebSocketHandler(sessionID, microserviceUUID))
	h, ok := handler.(*ExecSessionWebSocketHandler)
	if !ok {
		return nil
	}
	return h
}

// SetExecSessionManager sets the manager reference for session lifecycle callbacks.
func (h *ExecSessionWebSocketHandler) SetExecSessionManager(esm *ExecSessionManager) {
	h.execSessionManager = esm
}

func newExecSessionWebSocketHandler(sessionID, microserviceUUID string) *ExecSessionWebSocketHandler {
	cfg := config.GetInstance()
	controllerURL := cfg.ControllerURL
	if controllerURL == "" {
		logging.LogError(execWebSocketModuleName, "Controller URL is not configured", errors.New("controller URL is empty"))
		return nil
	}

	wsURL := convertToWebSocketURL(controllerURL)
	controllerWsURL := wsURL + "/agent/exec/microservice/" + microserviceUUID + "/" + sessionID

	ctx, cancel := context.WithCancel(context.Background())

	handler := &ExecSessionWebSocketHandler{
		controllerWsURL:  controllerWsURL,
		sessionID:        sessionID,
		microserviceUUID: microserviceUUID,
		outputBuffer:     make(chan execFrame, maxBufferedFrames),
		ctx:              ctx,
		cancel:           cancel,
		config:           cfg,
		jwtManager:       auth.GetJWTManager(),
	}

	handler.state.Store(StateDisconnected)
	handler.isConnected.Store(false)
	handler.isActive.Store(false)

	if strings.HasPrefix(strings.ToLower(controllerWsURL), "wss://") {
		handler.controllerTLS = buildControllerTLSConfig(cfg.SecureMode, cfg.ControllerCert, execWebSocketModuleName)
	}

	return handler
}

// convertToWebSocketURL converts HTTP/HTTPS URL to WebSocket URL
func convertToWebSocketURL(httpURL string) string {
	if strings.HasPrefix(httpURL, "http://") {
		return "ws://" + httpURL[7:]
	} else if strings.HasPrefix(httpURL, "https://") {
		return "wss://" + httpURL[8:]
	}
	return httpURL
}

// Reset tears down transport state and prepares the handler for a fresh Connect().
func (h *ExecSessionWebSocketHandler) Reset() {
	h.lifecycleMu.Lock()
	defer h.lifecycleMu.Unlock()
	h.resetLocked()
}

func (h *ExecSessionWebSocketHandler) resetLocked() {
	if h.cancel != nil {
		h.cancel()
	}

	closeWebSocketConn(&h.connMu, &h.conn)
	h.wg.Wait()
	stopSessionPingTicker(&h.pingTicker)

	h.isConnected.Store(false)
	h.isActive.Store(false)
	h.state.Store(StateDisconnected)
	drainExecOutputBuffer(h)
	h.resizeMu.Lock()
	h.resizeCh = nil
	h.resizeMu.Unlock()
	recreateHandlerContext(&h.ctx, &h.cancel)
}

// GetConnectionState returns the current connection state (for tests and diagnostics).
func (h *ExecSessionWebSocketHandler) GetConnectionState() ConnectionState {
	state, ok := h.state.Load().(ConnectionState)
	if !ok {
		return StateDisconnected
	}
	return state
}

// Connect establishes the WebSocket connection to the controller.
// Call Reset() before Connect() when starting a new exec session. No init pairing frame is sent.
func (h *ExecSessionWebSocketHandler) Connect() error {
	h.lifecycleMu.Lock()
	defer h.lifecycleMu.Unlock()

	if err := h.connectTransportLocked(); err != nil {
		return err
	}

	h.pingTicker = time.NewTicker(pingInterval)
	h.wg.Add(2)
	go h.pingWorker()
	go h.readWorker()

	logging.LogInfo(execWebSocketModuleName, fmt.Sprintf("WebSocket connection established for exec session: sessionId=%s, microserviceUuid=%s", h.sessionID, h.microserviceUUID))
	return nil
}

func (h *ExecSessionWebSocketHandler) connectTransportLocked() error {
	if h.isConnected.Load() {
		return errors.New("already connected; call Reset() before Connect()")
	}

	h.state.Store(StateDisconnected)

	if !h.transitionState(StateDisconnected, StateConnecting) {
		return fmt.Errorf("cannot connect: invalid state transition from %v", h.GetConnectionState())
	}

	h.connMu.Lock()
	defer h.connMu.Unlock()

	token, err := h.jwtManager.GenerateJWT()
	if err != nil {
		h.transitionState(StateConnecting, StateDisconnected)
		return fmt.Errorf("failed to generate JWT: %w", err)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: handshakeTimeout,
		TLSClientConfig:  h.controllerTLS,
	}

	headers := http.Header{}
	headers.Set("Authorization", "Bearer "+token)

	conn, resp, err := dialer.Dial(h.controllerWsURL, headers)
	if err != nil {
		h.transitionState(StateConnecting, StateDisconnected)
		if resp != nil {
			if cerr := resp.Body.Close(); cerr != nil {
				logging.LogWarn(execWebSocketModuleName, fmt.Sprintf("Failed to close response body: %v", cerr))
			}
		}
		return fmt.Errorf("failed to connect: %w", err)
	}
	if resp != nil {
		if cerr := resp.Body.Close(); cerr != nil {
			logging.LogWarn(execWebSocketModuleName, fmt.Sprintf("Failed to close response body: %v", cerr))
		}
	}

	h.conn = conn
	h.isConnected.Store(true)
	h.configureReadKeepalive(conn)

	if h.transitionState(StateConnecting, StatePending) {
		logging.LogInfo(execWebSocketModuleName, "Connection is now pending user activation")
	}

	return nil
}

func (h *ExecSessionWebSocketHandler) transitionState(from, to ConnectionState) bool {
	current, ok := h.state.Load().(ConnectionState)
	if !ok {
		return false
	}
	if current == from {
		h.state.Store(to)
		logging.LogDebug(execWebSocketModuleName, fmt.Sprintf("Connection state transition: %v -> %v", from, to))
		return true
	}
	return false
}

// needsResetBeforeConnect reports whether stale transport state must be torn down before Connect().
func (h *ExecSessionWebSocketHandler) needsResetBeforeConnect() bool {
	if h == nil {
		return false
	}
	if h.IsConnected() {
		return true
	}
	return h.GetConnectionState() != StateDisconnected
}

// SendMessage sends a message to the controller with execId = sessionId.
// Output is buffered while disconnected or pending activation.
func (h *ExecSessionWebSocketHandler) SendMessage(msgType byte, data []byte) error {
	if !h.isConnected.Load() || !h.isActive.Load() {
		logging.LogDebug(execWebSocketModuleName, fmt.Sprintf("Buffering output while connection is not active: connected=%v active=%v type=%d length=%d",
			h.isConnected.Load(), h.isActive.Load(), msgType, len(data)))
		return h.bufferOutput(msgType, data)
	}
	return h.writeOutboundMessage(msgType, data)
}

func (h *ExecSessionWebSocketHandler) bufferOutput(msgType byte, data []byte) error {
	if h.bufferedFrames.Load() >= maxBufferedFrames {
		logging.LogWarn(execWebSocketModuleName, "Buffer full, dropping frame")
		return errors.New("buffer full")
	}
	if h.bufferedSize.Load()+int64(len(data)) > maxBufferSize {
		logging.LogWarn(execWebSocketModuleName, "Buffer size limit reached, dropping frame")
		return errors.New("buffer size limit reached")
	}

	bufferedData := make([]byte, len(data))
	copy(bufferedData, data)

	select {
	case h.outputBuffer <- execFrame{msgType: msgType, data: bufferedData}:
		h.bufferedFrames.Add(1)
		h.bufferedSize.Add(int64(len(data)))
	default:
		logging.LogWarn(execWebSocketModuleName, "Buffer channel full, dropping frame")
		return errors.New("buffer channel full")
	}
	return nil
}

func (h *ExecSessionWebSocketHandler) writeOutboundMessage(msgType byte, data []byte) error {
	var buf bytes.Buffer
	enc := msgpack.NewEncoder(&buf)

	err := enc.EncodeMapLen(5)
	if err != nil {
		return fmt.Errorf("failed to encode map length: %w", err)
	}

	err = enc.EncodeString("type")
	if err != nil {
		return fmt.Errorf("failed to encode type key: %w", err)
	}
	err = enc.EncodeUint8(msgType)
	if err != nil {
		return fmt.Errorf("failed to encode type value: %w", err)
	}

	err = enc.EncodeString("data")
	if err != nil {
		return fmt.Errorf("failed to encode data key: %w", err)
	}
	err = enc.EncodeBytes(data)
	if err != nil {
		return fmt.Errorf("failed to encode data value: %w", err)
	}

	err = enc.EncodeString("microserviceUuid")
	if err != nil {
		return fmt.Errorf("failed to encode microserviceUuid key: %w", err)
	}
	err = enc.EncodeString(h.microserviceUUID)
	if err != nil {
		return fmt.Errorf("failed to encode microserviceUuid value: %w", err)
	}

	err = enc.EncodeString("execId")
	if err != nil {
		return fmt.Errorf("failed to encode execId key: %w", err)
	}
	err = enc.EncodeString(h.sessionID)
	if err != nil {
		return fmt.Errorf("failed to encode execId value: %w", err)
	}

	err = enc.EncodeString("timestamp")
	if err != nil {
		return fmt.Errorf("failed to encode timestamp key: %w", err)
	}
	err = enc.EncodeInt64(time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("failed to encode timestamp value: %w", err)
	}

	h.connMu.RLock()
	conn := h.conn
	h.connMu.RUnlock()

	if conn != nil {
		h.writeMu.Lock()
		defer h.writeMu.Unlock()
		err = conn.WriteMessage(websocket.BinaryMessage, buf.Bytes())
		if err != nil {
			return fmt.Errorf("failed to write message: %w", err)
		}
	}

	return nil
}

func (h *ExecSessionWebSocketHandler) readWaitDuration() time.Duration {
	if h.isActive.Load() {
		return 0
	}
	return execPendingReadTimeout
}

func (h *ExecSessionWebSocketHandler) extendReadDeadline(conn *websocket.Conn) error {
	if conn == nil {
		return nil
	}
	if h.isActive.Load() {
		return conn.SetReadDeadline(time.Time{})
	}
	return conn.SetReadDeadline(time.Now().Add(execPendingReadTimeout))
}

func (h *ExecSessionWebSocketHandler) configureReadKeepalive(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	extend := func() error {
		return h.extendReadDeadline(conn)
	}
	if err := extend(); err != nil {
		logging.LogWarn(execWebSocketModuleName, fmt.Sprintf("Failed to set read deadline: %v", err))
	}
	conn.SetPongHandler(func(string) error {
		return extend()
	})
	conn.SetPingHandler(func(appData string) error {
		if err := extend(); err != nil {
			return err
		}
		h.writeMu.Lock()
		defer h.writeMu.Unlock()
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
	})
}

func (h *ExecSessionWebSocketHandler) isPendingReadTimeout(err error) bool {
	if h.isActive.Load() {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func (h *ExecSessionWebSocketHandler) pingWorker() {
	defer h.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(execWebSocketModuleName, "Panic recovered", fmt.Errorf("%v", r))
		}
	}()
	defer func() {
		if h.pingTicker != nil {
			h.pingTicker.Stop()
		}
	}()

	for {
		select {
		case <-h.ctx.Done():
			return
		case <-h.pingTicker.C:
			if h.isConnected.Load() {
				h.connMu.RLock()
				conn := h.conn
				h.connMu.RUnlock()

				if conn != nil {
					h.writeMu.Lock()
					err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(time.Second))
					h.writeMu.Unlock()
					if err != nil {
						logging.LogError(execWebSocketModuleName, "Failed to send ping", err)
						go h.handleTransportClose()
					}
				}
			}
		}
	}
}

func (h *ExecSessionWebSocketHandler) readWorker() {
	defer h.wg.Done()
	defer func() {
		if r := recover(); r != nil {
			logging.LogError(execWebSocketModuleName, "Panic recovered", fmt.Errorf("%v", r))
		}
	}()

	for {
		select {
		case <-h.ctx.Done():
			return
		default:
			h.connMu.RLock()
			conn := h.conn
			h.connMu.RUnlock()

			if conn == nil {
				return
			}

			if err := h.extendReadDeadline(conn); err != nil {
				logging.LogWarn(execWebSocketModuleName, fmt.Sprintf("Failed to set read deadline: %v", err))
			}

			messageType, data, err := conn.ReadMessage()
			if err != nil {
				switch {
				case h.isPendingReadTimeout(err):
					logging.LogInfo(execWebSocketModuleName, fmt.Sprintf("Pending activation timeout for exec session: %s", h.sessionID))
				case websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway, 1005):
					logging.LogInfo(execWebSocketModuleName, fmt.Sprintf("WebSocket closed: %v", err))
				default:
					logging.LogError(execWebSocketModuleName, "WebSocket read error", err)
				}
				go h.handleTransportClose()
				return
			}

			switch messageType {
			case websocket.BinaryMessage:
				h.handleMessage(data)
			case websocket.PongMessage:
				logging.LogDebug(execWebSocketModuleName, "Received pong")
			default:
				logging.LogDebug(execWebSocketModuleName, fmt.Sprintf("Received message type: %d", messageType))
			}
		}
	}
}

func (h *ExecSessionWebSocketHandler) handleMessage(data []byte) {
	dec := msgpack.NewDecoder(bytes.NewReader(data))

	mapLen, err := dec.DecodeMapLen()
	if err != nil {
		logging.LogError(execWebSocketModuleName, "Failed to decode map length", err)
		return
	}

	var msgType byte
	var msgData []byte
	var msgMicroserviceUUID string
	var msgExecID string

	for i := 0; i < mapLen; i++ {
		key, err := dec.DecodeString()
		if err != nil {
			logging.LogError(execWebSocketModuleName, "Failed to decode key", err)
			return
		}

		switch key {
		case "type":
			val, err := dec.DecodeUint8()
			if err != nil {
				logging.LogError(execWebSocketModuleName, "Failed to decode type", err)
				return
			}
			msgType = val
		case "data":
			val, err := dec.DecodeBytes()
			if err != nil {
				logging.LogError(execWebSocketModuleName, "Failed to decode data", err)
				return
			}
			msgData = val
		case "microserviceUuid":
			val, err := dec.DecodeString()
			if err != nil {
				logging.LogError(execWebSocketModuleName, "Failed to decode microserviceUuid", err)
				return
			}
			msgMicroserviceUUID = val
		case "execId":
			val, err := dec.DecodeString()
			if err != nil {
				logging.LogError(execWebSocketModuleName, "Failed to decode execId", err)
				return
			}
			msgExecID = val
		case "timestamp":
			err = dec.Skip()
			if err != nil {
				logging.LogError(execWebSocketModuleName, "Failed to skip timestamp", err)
				return
			}
		default:
			err = dec.Skip()
			if err != nil {
				logging.LogError(execWebSocketModuleName, "Failed to skip value", err)
				return
			}
		}
	}

	if msgExecID != "" && msgExecID != h.sessionID {
		logging.LogDebug(execWebSocketModuleName, fmt.Sprintf("Ignoring message for other exec session: got=%s want=%s", msgExecID, h.sessionID))
		return
	}
	if msgMicroserviceUUID != "" && msgMicroserviceUUID != h.microserviceUUID {
		logging.LogDebug(execWebSocketModuleName, fmt.Sprintf("Ignoring message for other microservice: got=%s want=%s", msgMicroserviceUUID, h.microserviceUUID))
		return
	}

	switch msgType {
	case ExecTypeStdin:
		h.handleStdin(msgData)
	case ExecTypeControl:
		h.handleControl(msgData)
	case ExecTypeActivation:
		h.handleActivation(msgExecID)
	case ExecTypeClose:
		logging.LogInfo(execWebSocketModuleName, fmt.Sprintf("Received close message for exec session: %s", h.sessionID))
		h.handleSessionClose()
	default:
		logging.LogWarn(execWebSocketModuleName, fmt.Sprintf("Unknown message type: %d", msgType))
	}
}

func (h *ExecSessionWebSocketHandler) handleStdin(data []byte) {
	if data == nil {
		return
	}

	esm := h.execSessionManager
	if esm == nil {
		esm = GetExecSessionManager()
	}
	callback := esm.GetCallback(h.sessionID)
	if callback == nil {
		logging.LogWarn(execWebSocketModuleName, fmt.Sprintf("No active callback found for exec session: %s", h.sessionID))
		return
	}

	h.activateIfPending()

	if err := callback.WriteInput(data); err != nil {
		logging.LogError(execWebSocketModuleName, "Error writing input to exec callback", err)
	}
}

func parseExecResize(data []byte) (uint32, uint32, bool) {
	var ctrl execResizeControl
	if err := json.Unmarshal(data, &ctrl); err != nil {
		return 0, 0, false
	}
	if !strings.EqualFold(strings.TrimSpace(ctrl.Type), "resize") {
		return 0, 0, false
	}
	if ctrl.Cols < minExecCols || ctrl.Cols > maxExecCols || ctrl.Rows < minExecRows || ctrl.Rows > maxExecRows {
		return 0, 0, false
	}
	return ctrl.Cols, ctrl.Rows, true
}

func (h *ExecSessionWebSocketHandler) handleControl(data []byte) {
	if data == nil {
		return
	}

	if cols, rows, ok := parseExecResize(data); ok {
		h.requestResize(cols, rows)
		return
	}

	controlCmd := string(data)
	if controlCmd == "close" {
		logging.LogInfo(execWebSocketModuleName, fmt.Sprintf("Received close control command for exec session: %s", h.sessionID))
		h.handleSessionClose()
		return
	}

	logging.LogWarn(execWebSocketModuleName, fmt.Sprintf("Unknown control command: %s", controlCmd))
}

func (h *ExecSessionWebSocketHandler) requestResize(cols, rows uint32) {
	h.resizeMu.Lock()
	ctx := h.ctx
	if ctx == nil || ctx.Err() != nil {
		h.resizeMu.Unlock()
		return
	}
	if h.resizeCh == nil {
		h.resizeCh = make(chan resizeReq, 1)
		go h.resizeWorker(ctx, h.resizeCh)
	}
	ch := h.resizeCh
	h.resizeMu.Unlock()

	req := resizeReq{cols: cols, rows: rows}
	select {
	case ch <- req:
		return
	default:
	}
	select {
	case <-ch:
	default:
	}
	select {
	case ch <- req:
	default:
	}
}

func (h *ExecSessionWebSocketHandler) resizeWorker(ctx context.Context, ch <-chan resizeReq) {
	var pending *resizeReq
	attempts := 0
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()

	schedule := func(delay time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
	}

	for {
		select {
		case <-ctx.Done():
			return
		case req := <-ch:
			copied := req
			pending = &copied
			attempts = 0
			schedule(time.Millisecond)
		case <-timer.C:
			if pending == nil {
				continue
			}
			err := h.applyResize(pending.cols, pending.rows)
			if err == nil {
				pending = nil
				attempts = 0
				continue
			}
			attempts++
			if attempts >= execResizeRetryAttempts {
				logging.LogWarn(execWebSocketModuleName, fmt.Sprintf(
					"Giving up terminal resize for session %s (%dx%d): %v",
					h.sessionID, pending.cols, pending.rows, err,
				))
				pending = nil
				attempts = 0
				continue
			}
			schedule(execResizeRetryInterval)
		}
	}
}

func (h *ExecSessionWebSocketHandler) applyResize(cols, rows uint32) error {
	esm := h.execSessionManager
	if esm == nil {
		esm = GetExecSessionManager()
	}
	if esm == nil {
		return errExecResizeNotReady
	}

	esm.mu.RLock()
	pm := esm.processManager
	esm.mu.RUnlock()
	if pm == nil {
		return errExecResizeNotReady
	}

	rec, ok := pm.GetSession(h.sessionID)
	if !ok || rec == nil || strings.TrimSpace(rec.RuntimeExecID) == "" || !rec.Started {
		return errExecResizeNotReady
	}
	return pm.ResizeExecSession(rec.RuntimeExecID, cols, rows)
}

func (h *ExecSessionWebSocketHandler) handleActivation(execID string) {
	if execID != "" && execID != h.sessionID {
		logging.LogWarn(execWebSocketModuleName, fmt.Sprintf("Received activation for other exec session: got=%s want=%s", execID, h.sessionID))
		return
	}

	logging.LogInfo(execWebSocketModuleName, fmt.Sprintf("Handling ACTIVATION for exec session: %s", h.sessionID))
	h.activateIfPending()
}

func (h *ExecSessionWebSocketHandler) activateIfPending() {
	if h.isActive.Load() {
		return
	}
	if h.transitionState(StatePending, StateActive) || h.transitionState(StateConnected, StateActive) {
		h.isActive.Store(true)
		h.connMu.RLock()
		conn := h.conn
		h.connMu.RUnlock()
		if err := h.extendReadDeadline(conn); err != nil {
			logging.LogWarn(execWebSocketModuleName, fmt.Sprintf("Failed to extend read deadline on activation: %v", err))
		}
		go h.flushBuffer()
	}
}

func (h *ExecSessionWebSocketHandler) flushBuffer() {
	for {
		select {
		case frame := <-h.outputBuffer:
			h.bufferedFrames.Add(-1)
			h.bufferedSize.Add(-int64(len(frame.data)))
			if err := h.writeOutboundMessage(frame.msgType, frame.data); err != nil {
				logging.LogError(execWebSocketModuleName, "Failed to send buffered message", err)
			}
		default:
			return
		}
	}
}

func (h *ExecSessionWebSocketHandler) gracefulCloseTransportLocked(reason string) {
	h.connMu.Lock()
	defer h.connMu.Unlock()
	if h.conn == nil {
		return
	}
	h.writeMu.Lock()
	deadline := time.Now().Add(time.Second)
	_ = h.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, reason), deadline)
	h.writeMu.Unlock()
	_ = h.conn.Close()
	h.conn = nil
}

// Disconnect closes the WebSocket connection and resets handler state for reuse.
func (h *ExecSessionWebSocketHandler) Disconnect() {
	h.lifecycleMu.Lock()
	defer h.lifecycleMu.Unlock()
	h.gracefulCloseTransportLocked(execCloseReasonSessionStopped)
	h.resetLocked()
	logging.LogInfo(execWebSocketModuleName, fmt.Sprintf("Disconnected WebSocket for exec session: %s", h.sessionID))
}

// IsConnected returns whether the WebSocket is connected
func (h *ExecSessionWebSocketHandler) IsConnected() bool {
	return h.isConnected.Load()
}

// IsActive returns whether the exec session is active
func (h *ExecSessionWebSocketHandler) IsActive() bool {
	return h.isActive.Load()
}

func (h *ExecSessionWebSocketHandler) handleTransportClose() {
	if !h.isConnected.Load() {
		return
	}

	logging.LogInfo(execWebSocketModuleName, fmt.Sprintf("Transport closed for exec session: %s (shell retained for reconnect)", h.sessionID))
	h.isConnected.Store(false)
	h.isActive.Store(false)
	h.state.Store(StateDisconnected)
	h.Reset()
}

func (h *ExecSessionWebSocketHandler) handleSessionClose() {
	esm := h.execSessionManager
	if esm == nil {
		esm = GetExecSessionManager()
	}
	sessionID := h.sessionID
	go esm.StopExecSession(sessionID)
}
