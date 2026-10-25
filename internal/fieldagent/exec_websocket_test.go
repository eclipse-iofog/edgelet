package fieldagent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/gorilla/websocket"
	"github.com/vmihailenco/msgpack/v5"
)

func websocketUpgrader() websocket.Upgrader {
	return websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
}

func TestConvertToWebSocketURL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "HTTP URL",
			input:    "http://controller.example.com:54421/api/v2/",
			expected: "ws://controller.example.com:54421/api/v2/",
		},
		{
			name:     "HTTPS URL",
			input:    "https://controller.example.com:54421/api/v2/",
			expected: "wss://controller.example.com:54421/api/v2/",
		},
		{
			name:     "Already WebSocket URL",
			input:    "ws://controller.example.com:54421/api/v2/",
			expected: "ws://controller.example.com:54421/api/v2/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := convertToWebSocketURL(tt.input)
			if result != tt.expected {
				t.Errorf("convertToWebSocketURL(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func setupExecTestJWT(t *testing.T) {
	t.Helper()

	db := store.GetInstance()
	if err := db.Open(t.TempDir()); err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	_, privateKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	seed := privateKey.Seed()
	jwk := map[string]any{
		"kty": "OKP",
		"crv": "Ed25519",
		"d":   base64.RawURLEncoding.EncodeToString(seed),
		"x":   base64.RawURLEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey)),
	}
	jwkJSON, err := json.Marshal(jwk)
	if err != nil {
		t.Fatalf("marshal jwk: %v", err)
	}

	cfg := config.GetInstance()
	cfg.PrivateKey = base64.StdEncoding.EncodeToString(jwkJSON)
	cfg.IOFogUUID = "test-agent-uuid"
	cfg.SecureMode = false

	auth.GetJWTManager().Reset()
}

func newTestExecHandler(wsURL, sessionID, microserviceUUID string) *ExecSessionWebSocketHandler {
	ctx, cancel := context.WithCancel(context.Background())
	h := &ExecSessionWebSocketHandler{
		controllerWsURL:  wsURL,
		sessionID:        sessionID,
		microserviceUUID: microserviceUUID,
		outputBuffer:     make(chan execFrame, maxBufferedFrames),
		ctx:              ctx,
		cancel:           cancel,
		config:           config.GetInstance(),
		jwtManager:       auth.GetJWTManager(),
	}
	h.state.Store(StateDisconnected)
	h.isConnected.Store(false)
	h.isActive.Store(false)
	return h
}

func TestExecHandler_ResetClearsStalePendingState(t *testing.T) {
	h := newTestExecHandler("ws://unused", "sess-reset", "ms-stale")
	h.isConnected.Store(false)
	h.state.Store(StatePending)

	h.Reset()

	if got := h.GetConnectionState(); got != StateDisconnected {
		t.Fatalf("state = %v, want StateDisconnected", got)
	}
	if h.IsConnected() {
		t.Fatal("expected not connected after reset")
	}
	if h.IsActive() {
		t.Fatal("expected not active after reset")
	}
}

func TestExecHandler_DisconnectClearsStalePendingState(t *testing.T) {
	h := newTestExecHandler("ws://unused", "sess-disconnect", "ms-disconnect")
	h.isConnected.Store(false)
	h.state.Store(StatePending)

	h.Disconnect()

	if got := h.GetConnectionState(); got != StateDisconnected {
		t.Fatalf("state = %v, want StateDisconnected", got)
	}
}

func TestExecHandler_ConnectAfterStalePendingState(t *testing.T) {
	setupExecTestJWT(t)

	var upgrades atomic.Int32
	upgrader := websocketUpgrader()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upgrades.Add(1)
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, _, _ = conn.ReadMessage()
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}))
	t.Cleanup(srv.Close)

	sessionID := "sess-connect"
	msUUID := "ms-connect"
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/exec/microservice/" + msUUID + "/" + sessionID
	h := newTestExecHandler(wsURL, sessionID, msUUID)
	h.isConnected.Store(false)
	h.state.Store(StatePending)

	if err := h.Connect(); err != nil {
		t.Fatalf("Connect after stale pending state: %v", err)
	}
	if upgrades.Load() != 1 {
		t.Fatalf("expected 1 websocket upgrade, got %d", upgrades.Load())
	}
	if h.GetConnectionState() != StatePending {
		t.Fatalf("state = %v, want StatePending after connect", h.GetConnectionState())
	}

	h.Reset()

	if err := h.Connect(); err != nil {
		t.Fatalf("second Connect after reset: %v", err)
	}
	if upgrades.Load() != 2 {
		t.Fatalf("expected 2 websocket upgrades after retry, got %d", upgrades.Load())
	}

	h.Reset()
}

func TestExecHandler_ConnectDoesNotSendInitMessage(t *testing.T) {
	setupExecTestJWT(t)

	initCh := make(chan struct{}, 1)
	upgrader := websocketUpgrader()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if _, _, err := conn.ReadMessage(); err == nil {
			initCh <- struct{}{}
		}
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}))
	t.Cleanup(srv.Close)

	sessionID := "sess-no-init"
	msUUID := "ms-init"
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/exec/microservice/" + msUUID + "/" + sessionID
	h := newTestExecHandler(wsURL, sessionID, msUUID)

	if err := h.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case <-initCh:
		t.Fatal("unexpected init/pairing frame on connect")
	case <-time.After(600 * time.Millisecond):
	}

	h.Reset()
}

func TestExecHandler_SendMessageUsesSessionIDAsExecID(t *testing.T) {
	setupExecTestJWT(t)

	var received atomic.Bool
	upgrader := websocketUpgrader()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		dec := msgpack.NewDecoder(bytes.NewReader(data))
		n, err := dec.DecodeMapLen()
		if err != nil {
			return
		}
		for i := 0; i < n; i++ {
			key, err := dec.DecodeString()
			if err != nil {
				return
			}
			if key == "execId" {
				val, err := dec.DecodeString()
				if err != nil {
					return
				}
				if val == "sess-send" {
					received.Store(true)
				}
				return
			}
			if err := dec.Skip(); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	sessionID := "sess-send"
	msUUID := "ms-send"
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/exec/microservice/" + msUUID + "/" + sessionID
	h := newTestExecHandler(wsURL, sessionID, msUUID)
	if err := h.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	h.isActive.Store(true)
	h.state.Store(StateActive)

	if err := h.SendMessage(ExecTypeStdout, []byte("hello")); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for !received.Load() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for outbound frame with execId=sessionId")
		}
		time.Sleep(20 * time.Millisecond)
	}

	h.Reset()
}

func TestExecHandler_ResetIsolationBetweenSessions(t *testing.T) {
	hA := newTestExecHandler("ws://unused/a", "sess-a", "ms-a")
	hB := newTestExecHandler("ws://unused/b", "sess-b", "ms-b")

	hA.isConnected.Store(false)
	hA.state.Store(StatePending)
	hA.Reset()

	if hB.GetConnectionState() != StateDisconnected {
		t.Fatalf("sess-b state changed unexpectedly: %v", hB.GetConnectionState())
	}

	hB.state.Store(StateActive)
	hB.isConnected.Store(true)
}

func TestExecHandler_needsResetBeforeConnect(t *testing.T) {
	h := newTestExecHandler("ws://unused", "sess-needs-reset", "ms-1")
	if h.needsResetBeforeConnect() {
		t.Fatal("fresh handler should not need reset")
	}
	h.state.Store(StatePending)
	if !h.needsResetBeforeConnect() {
		t.Fatal("pending handler should need reset")
	}
	h.state.Store(StateDisconnected)
	h.isConnected.Store(true)
	if !h.needsResetBeforeConnect() {
		t.Fatal("connected handler should need reset")
	}
}

func TestExecHandler_SendMessageBuffersWhilePending(t *testing.T) {
	h := newTestExecHandler("ws://unused", "sess-buffer", "ms-buffer")
	h.isConnected.Store(true)
	h.state.Store(StatePending)

	if err := h.SendMessage(ExecTypeStdout, []byte("early")); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if h.bufferedFrames.Load() != 1 {
		t.Fatalf("bufferedFrames = %d, want 1", h.bufferedFrames.Load())
	}
}

func TestExecHandler_SendMessageBuffersWhileDisconnected(t *testing.T) {
	h := newTestExecHandler("ws://unused", "sess-disc", "ms-disc")
	h.isActive.Store(true)
	h.state.Store(StateActive)

	if err := h.SendMessage(ExecTypeStdout, []byte("offline")); err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if h.bufferedFrames.Load() != 1 {
		t.Fatalf("bufferedFrames = %d, want 1", h.bufferedFrames.Load())
	}
}

func TestExecHandler_ActivationFlushesBufferedStdout(t *testing.T) {
	setupExecTestJWT(t)

	received := make(chan []byte, 1)
	upgrader := websocketUpgrader()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			dec := msgpack.NewDecoder(bytes.NewReader(data))
			n, err := dec.DecodeMapLen()
			if err != nil {
				continue
			}
			var msgType byte
			var payload []byte
			for i := 0; i < n; i++ {
				key, err := dec.DecodeString()
				if err != nil {
					break
				}
				switch key {
				case "type":
					v, err := dec.DecodeUint8()
					if err == nil {
						msgType = v
					}
				case "data":
					payload, _ = dec.DecodeBytes()
				default:
					_ = dec.Skip()
				}
			}
			if msgType == ExecTypeStdout && len(payload) > 0 {
				select {
				case received <- payload:
				default:
				}
				return
			}
		}
	}))
	t.Cleanup(srv.Close)

	sessionID := "sess-flush"
	msUUID := "ms-flush"
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent/exec/microservice/" + msUUID + "/" + sessionID
	h := newTestExecHandler(wsURL, sessionID, msUUID)
	if err := h.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := h.SendMessage(ExecTypeStdout, []byte("buffered-prompt")); err != nil {
		t.Fatalf("SendMessage before activation: %v", err)
	}
	if h.bufferedFrames.Load() != 1 {
		t.Fatalf("expected 1 buffered frame before activation, got %d", h.bufferedFrames.Load())
	}

	h.handleActivation(sessionID)

	select {
	case payload := <-received:
		if string(payload) != "buffered-prompt" {
			t.Fatalf("payload = %q, want buffered-prompt", payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for flushed stdout after activation")
	}
	if h.bufferedFrames.Load() != 0 {
		t.Fatalf("bufferedFrames = %d after flush, want 0", h.bufferedFrames.Load())
	}

	h.Reset()
}

func TestExecCallback_ForwardBuffersBeforeConnect(t *testing.T) {
	sessionID := "sess-callback"
	msUUID := "ms-callback"
	handler := newTestExecHandler("ws://unused", sessionID, msUUID)
	callback := &ExecSessionCallback{
		microserviceUUID: msUUID,
		execID:           sessionID,
		webSocketHandler: handler,
	}
	callback.isRunning.Store(true)

	callback.forwardToWebSocket(ExecTypeStdout, []byte("pre-connect"))
	if handler.bufferedFrames.Load() != 1 {
		t.Fatalf("bufferedFrames = %d, want 1", handler.bufferedFrames.Load())
	}
}

func TestExecHandler_readWaitDuration(t *testing.T) {
	h := newTestExecHandler("ws://unused", "sess-duration", "ms-duration")
	if got := h.readWaitDuration(); got != execPendingReadTimeout {
		t.Fatalf("pending readWaitDuration = %v, want %v", got, execPendingReadTimeout)
	}
	h.isActive.Store(true)
	if got := h.readWaitDuration(); got != 0 {
		t.Fatalf("active readWaitDuration = %v, want 0 (disabled)", got)
	}
}

func TestParseExecResize(t *testing.T) {
	cols, rows, ok := parseExecResize([]byte(`{"type":"resize","cols":120,"rows":32}`))
	if !ok || cols != 120 || rows != 32 {
		t.Fatalf("parseExecResize() = (%d, %d, %v), want (120, 32, true)", cols, rows, ok)
	}

	if _, _, ok := parseExecResize([]byte(`{"type":"RESIZE","cols":80,"rows":24}`)); !ok {
		t.Fatal("expected case-insensitive resize type")
	}

	cases := []string{
		`{"type":"resize","cols":1,"rows":24}`,
		`{"type":"resize","cols":80,"rows":0}`,
		`{"type":"resize","cols":4097,"rows":24}`,
		`{"type":"resize","cols":80,"rows":4097}`,
		"close",
		"keepalive",
		`{"type":"keepalive"}`,
		"",
	}
	for _, raw := range cases {
		if _, _, ok := parseExecResize([]byte(raw)); ok {
			t.Fatalf("parseExecResize(%q) = ok, want reject", raw)
		}
	}
}

func TestApplyResizeBeforeSessionStarted(t *testing.T) {
	pm := &processmanager.ProcessManager{}
	esm := &ExecSessionManager{processManager: pm}
	handler := &ExecSessionWebSocketHandler{
		sessionID:          "sess-1",
		execSessionManager: esm,
	}
	if err := handler.applyResize(80, 24); err == nil {
		t.Fatal("expected resize to wait until the exec session is started")
	}

	handler.execSessionManager = &ExecSessionManager{}
	if err := handler.applyResize(80, 24); err == nil {
		t.Fatal("expected resize to fail when the process manager is unset")
	}
}

func TestExecCallback_execTimeoutIs24Hours(t *testing.T) {
	if execTimeout != ControllerSessionMaxDuration {
		t.Fatalf("execTimeout = %v, want %v", execTimeout, ControllerSessionMaxDuration)
	}
	if execTimeout != 24*time.Hour {
		t.Fatalf("execTimeout = %v, want 24h", execTimeout)
	}
}
