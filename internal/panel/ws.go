package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/url"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/cedar2025/xboard-node/internal/nlog"
	"github.com/gorilla/websocket"
)

// WSEvent types
const (
	WSEventSyncConfig    = "sync.config"
	WSEventSyncUsers     = "sync.users"
	WSEventSyncUserDelta = "sync.user.delta"
	WSEventSyncDevices   = "sync.devices"   // panel → node: global device state
	WSEventSyncNodes     = "sync.nodes"     // panel → machine: node list changed
	WSEventReportDevices = "report.devices" // node → panel: report device snapshot
)

// WSEvent is a parsed data event delivered to the service layer.
type WSEvent struct {
	Type        string
	Config      *NodeConfig
	Users       []User
	DeltaAction string // "add" or "remove" (only for sync.user.delta)
	DeltaUsers  []User // users affected by the delta

	// Device sync fields
	DeviceUsers map[int][]string // userID -> IPs (for sync.devices)
	NodeID      int

	// Machine node discovery fields (for sync.nodes)
	Nodes []MachineNode
}

// WSStatusChange notifies the service when WS connectivity changes.
type WSStatusChange struct {
	Connected bool
}

// wsMessage is the JSON envelope for all WS messages.
type wsMessage struct {
	Event     string          `json:"event"`
	Data      json.RawMessage `json:"data,omitempty"`
	Timestamp int64           `json:"timestamp,omitempty"`
}

// Payload structures for data events.
type syncConfigPayload struct {
	Config    NodeConfig `json:"config"`
	Timestamp int64      `json:"timestamp"`
	NodeID    int        `json:"node_id"`
}

type syncUsersPayload struct {
	Users     []User `json:"users"`
	Timestamp int64  `json:"timestamp"`
	NodeID    int    `json:"node_id"`
}

type syncUserDeltaPayload struct {
	Action    string `json:"action"`
	Users     []User `json:"users"`
	Timestamp int64  `json:"timestamp"`
	NodeID    int    `json:"node_id"`
}

// syncDevicesPayload carries global device state from panel.
type syncDevicesPayload struct {
	Users     interface{} `json:"users"`
	Timestamp int64       `json:"timestamp"`
	NodeID    int         `json:"node_id"`
}

// decodeDeviceUsers 解析面板发送的设备快照，兼容 PHP 稀疏索引数组。
func decodeDeviceUsers(value interface{}) (map[int][]string, error) {
	if value == nil {
		return nil, nil
	}
	if list, ok := value.([]interface{}); ok && len(list) == 0 {
		return map[int][]string{}, nil
	}
	users, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("users: invalid type %T", value)
	}
	result := make(map[int][]string, len(users))
	for key, value := range users {
		id, err := strconv.Atoi(key)
		if err != nil {
			return nil, fmt.Errorf("invalid user ID %q", key)
		}
		if _, exists := result[id]; exists {
			return nil, fmt.Errorf("duplicate user ID %q", key)
		}
		ips, err := decodeDeviceIPs(value)
		if err != nil {
			return nil, fmt.Errorf("user %q: %w", key, err)
		}
		result[id] = ips
	}
	return result, nil
}

// decodeDeviceIPs 按数值索引顺序解析设备 IP 列表。
func decodeDeviceIPs(value interface{}) ([]string, error) {
	var items []interface{}
	switch list := value.(type) {
	case []interface{}:
		items = list
	case map[string]interface{}:
		keys := make([]int, 0, len(list))
		for key := range list {
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || strconv.Itoa(index) != key {
				return nil, fmt.Errorf("invalid device index %q", key)
			}
			keys = append(keys, index)
		}
		sort.Ints(keys)
		for _, index := range keys {
			items = append(items, list[strconv.Itoa(index)])
		}
	default:
		return nil, fmt.Errorf("invalid device list type %T", value)
	}
	ips := make([]string, 0, len(items))
	for _, item := range items {
		ip, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("invalid device IP type %T", item)
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

// syncNodesPayload carries the updated node list for a machine.
type syncNodesPayload struct {
	Nodes []MachineNode `json:"nodes"`
}

// WSClientConfig holds WebSocket client tuning options.
type WSClientConfig struct {
	StatusInterval   time.Duration
	HandshakeTimeout time.Duration
	BackoffInitial   time.Duration
	BackoffMax       time.Duration
	MachineID        int
}

// WSClient connects to the panel's Workerman WS server using native WebSocket.
// Authentication is done via query parameters (token + node_id) during the
// WebSocket handshake — no separate auth step needed.
type WSClient struct {
	wsURL    string // base WS URL, e.g. ws://panel.example.com:8076
	token    string
	nodeID   int
	onEvent  func(WSEvent)
	onStatus func(WSStatusChange)
	onPing   func() map[string]interface{}

	cfg WSClientConfig

	connected          atomic.Bool
	notifiedDisconnect atomic.Bool

	// writeCh allows sending messages from outside the connect loop.
	// Stored atomically so concurrent senders never race with reconnects.
	writeCh atomic.Pointer[chan wsMessage]
}

// NewWSClient creates a new WebSocket client.
// wsURL is the base WebSocket URL (e.g. "ws://panel.example.com:8076").
// token and nodeID are used for authentication via query parameters.
func NewWSClient(wsURL string, token string, nodeID int, cfg WSClientConfig, onEvent func(WSEvent), onStatus func(WSStatusChange), onPing func() map[string]interface{}) *WSClient {
	// Apply defaults
	if cfg.StatusInterval == 0 {
		cfg.StatusInterval = 10 * time.Second
	}
	if cfg.HandshakeTimeout == 0 {
		cfg.HandshakeTimeout = 15 * time.Second
	}
	if cfg.BackoffInitial == 0 {
		cfg.BackoffInitial = time.Second
	}
	if cfg.BackoffMax == 0 {
		cfg.BackoffMax = 60 * time.Second
	}
	return &WSClient{
		wsURL:    wsURL,
		token:    token,
		nodeID:   nodeID,
		cfg:      cfg,
		onEvent:  onEvent,
		onStatus: onStatus,
		onPing:   onPing,
	}
}

func (w *WSClient) IsConnected() bool { return w.connected.Load() }

func (w *WSClient) notifyStatus(connected bool) {
	if connected {
		w.notifiedDisconnect.Store(false)
	} else if !w.notifiedDisconnect.CompareAndSwap(false, true) {
		return
	}
	if w.onStatus != nil {
		w.onStatus(WSStatusChange{Connected: connected})
	}
}

// Run connects and reconnects until ctx is cancelled.
func (w *WSClient) Run(ctx context.Context) {
	backoff := w.cfg.BackoffInitial
	for {
		start := time.Now()
		err := w.connect(ctx)

		wasConnected := w.connected.Swap(false)
		if ctx.Err() != nil {
			return
		}
		if wasConnected {
			w.notifyStatus(false)
		}

		if err != nil {
			nlog.Core().Warn("ws disconnected", "error", err)
			if !wasConnected {
				w.notifyStatus(false)
			}
		}

		// Reset backoff if the connection was up for a meaningful duration.
		if time.Since(start) > 2*time.Minute {
			backoff = w.cfg.BackoffInitial
		}

		// Apply exponential backoff with jitter to prevent thundering herd.
		var jitter time.Duration
		if maxJitter := int64(backoff / 5); maxJitter > 0 {
			jitter = time.Duration(rand.Int63n(maxJitter))
		}
		wait := backoff + jitter

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if backoff < w.cfg.BackoffMax {
			backoff = min(backoff*2, w.cfg.BackoffMax)
		}
	}
}

func (w *WSClient) connect(ctx context.Context) error {
	u, err := url.Parse(w.wsURL)
	if err != nil {
		return fmt.Errorf("parse ws url: %w", err)
	}
	q := u.Query()
	q.Set("token", w.token)
	if w.cfg.MachineID > 0 {
		q.Set("machine_id", strconv.Itoa(w.cfg.MachineID))
	} else {
		q.Set("node_id", strconv.Itoa(w.nodeID))
	}
	u.RawQuery = q.Encode()

	nlog.Core().Debug("ws connecting", "url", u.String())

	dialer := websocket.Dialer{
		HandshakeTimeout: w.cfg.HandshakeTimeout,
	}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.Close()
	// 认证读取也可能永久阻塞，取消时直接关闭底层连接唤醒读取。
	stopClose := context.AfterFunc(ctx, func() { conn.Close() })
	defer stopClose()

	conn.SetReadLimit(10 << 20) // 10MB max message size

	// 复用握手超时限制首次认证响应，成功读取后清除期限。
	if err := conn.SetReadDeadline(time.Now().Add(w.cfg.HandshakeTimeout)); err != nil {
		return fmt.Errorf("set auth read deadline: %w", err)
	}
	var firstMsg wsMessage
	if err := conn.ReadJSON(&firstMsg); err != nil {
		return fmt.Errorf("read auth response: %w", err)
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear auth read deadline: %w", err)
	}
	nlog.Core().Debug("ws recv", "event", firstMsg.Event, "data", string(firstMsg.Data))

	if firstMsg.Event == "error" {
		var errData struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(firstMsg.Data, &errData); err != nil {
			return fmt.Errorf("auth failed (unable to parse error: %v)", err)
		}
		return fmt.Errorf("auth failed: %s", errData.Message)
	}

	if firstMsg.Event != "auth.success" {
		// It might be a data event already (server pushed sync before auth.success)
		// Process it and continue
		w.connected.Store(true)
		w.notifyStatus(true)
		w.handleMessage(firstMsg)
	} else {
		w.connected.Store(true)
		w.notifyStatus(true)
	}

	// Establish rolling read deadline so silent network drops (NAT/firewall idle
	// drops without TCP FIN/RST) cannot hang ReadJSON forever, especially in
	// machine mode where onPing is nil. Floor at 90s so panels/proxies that only
	// send 60s application-level JSON pings (without RFC6455 Pong) never falsely drop.
	readTimeout := w.cfg.StatusInterval * 6
	if readTimeout < 90*time.Second {
		readTimeout = 90 * time.Second
	}
	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return fmt.Errorf("set rolling read deadline: %w", err)
	}
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(readTimeout))
	})

	// Ping interval: send WS keepalive pings and periodic status pushes.
	reportTicker := time.NewTicker(w.cfg.StatusInterval)
	defer reportTicker.Stop()

	// writeCh decouples data collection from network I/O.
	writeCh := make(chan wsMessage, 16)
	w.writeCh.Store(&writeCh)
	defer func() { w.writeCh.Store(nil) }()

	errCh := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var msg wsMessage
			if err := conn.ReadJSON(&msg); err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
			_ = conn.SetReadDeadline(time.Now().Add(readTimeout))
			nlog.Core().Debug("ws recv", "event", msg.Event, "data", string(msg.Data))
			w.handleMessage(msg)
			if msg.Event == "ping" {
				select {
				case writeCh <- wsMessage{Event: "pong"}:
				default:
					nlog.Core().Warn("ws write channel full, skipping pong")
				}
			}
		}
	}()
	// Ensure the read goroutine has exited on ALL return paths (including
	// WriteJSON / Ping errors) before connect() returns, preventing stale
	// onEvent callbacks from racing with subsequent disconnect/reconnect state.
	defer func() {
		conn.Close()
		<-done
	}()

	for {
		select {
		case <-ctx.Done():
			return nil

		case err := <-errCh:
			return fmt.Errorf("read: %w", err)

		case <-reportTicker.C:
			// Send WS protocol-level Ping to detect silent half-open drops.
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return fmt.Errorf("ping: %w", err)
			}
			// Send periodic node.status via WebSocket
			if w.onPing != nil {
				msg := wsMessage{Event: "node.status"}
				if stats := w.onPing(); stats != nil {
					data, _ := json.Marshal(stats)
					msg.Data = data
					msg.Timestamp = time.Now().Unix()
				}
				select {
				case writeCh <- msg:
				default:
					nlog.Core().Warn("ws write channel full, skipping status push (network slow?)")
				}
			}

		case msg := <-writeCh:
			// Perform the actual network write asynchronously in this loop.
			nlog.Core().Debug("ws send", "event", msg.Event, "data", string(msg.Data))
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(msg); err != nil {
				return fmt.Errorf("write: %w", err)
			}
		}
	}
}

func (w *WSClient) handleMessage(msg wsMessage) {
	switch msg.Event {
	case "ping":
		// Server ping — handled by the pong timer reset above
		nlog.Core().Debug("ws received ping")

	case "auth.success":
		nlog.Core().Debug("ws auth confirmed")

	case WSEventSyncConfig:
		w.handleDataEvent(msg)

	case WSEventSyncUsers:
		w.handleDataEvent(msg)

	case WSEventSyncUserDelta:
		w.handleDataEvent(msg)

	case WSEventSyncDevices:
		w.handleDataEvent(msg)

	case WSEventSyncNodes:
		w.handleDataEvent(msg)

	default:
		nlog.Core().Debug("ws unknown event", "event", msg.Event)
	}
}

func (w *WSClient) handleDataEvent(msg wsMessage) {
	var event WSEvent
	event.Type = msg.Event

	// Helper to unmarshal and decode with weak Typing
	decodeData := func(data []byte, target interface{}) error {
		var raw map[string]interface{}
		if err := json.Unmarshal(data, &raw); err != nil {
			return err
		}
		return decodeWeakRaw(raw, target)
	}

	switch msg.Event {
	case WSEventSyncConfig:
		nlog.Core().Debug("ws sync config event received")
		var p syncConfigPayload
		if err := decodeData(msg.Data, &p); err != nil {
			nlog.Core().Warn("ws: cannot decode config payload", "error", err)
			return
		}
		if p.Config.Protocol == "" {
			nlog.Core().Warn("ws: config payload missing protocol")
			return
		}
		event.Config = &p.Config
		if p.NodeID > 0 {
			event.NodeID = p.NodeID
		} else if p.Config.NodeID > 0 {
			event.NodeID = p.Config.NodeID
		}

	case WSEventSyncUsers:
		nlog.Core().Debug("ws sync users event received")
		// 只有显式数组才是有效快照，避免缺失或 null 清空现有用户。
		var raw struct {
			Users json.RawMessage `json:"users"`
		}
		var users []json.RawMessage
		if err := json.Unmarshal(msg.Data, &raw); err != nil || json.Unmarshal(raw.Users, &users) != nil || users == nil {
			nlog.Core().Warn("ws: users payload missing valid array")
			return
		}
		var p syncUsersPayload
		if err := decodeData(msg.Data, &p); err != nil {
			nlog.Core().Warn("ws: cannot decode users payload", "error", err)
			return
		}
		if p.Users == nil {
			p.Users = []User{}
		}
		event.Users = p.Users
		event.NodeID = p.NodeID

	case WSEventSyncUserDelta:
		nlog.Core().Debug("ws sync user delta event received")
		var p syncUserDeltaPayload
		if err := decodeData(msg.Data, &p); err != nil {
			nlog.Core().Warn("ws: cannot decode user delta payload", "error", err)
			return
		}
		if p.Action == "" {
			nlog.Core().Warn("ws: user delta payload missing action")
			return
		}
		if len(p.Users) == 0 {
			nlog.Core().Warn("ws: user delta payload has no users")
			return
		}
		event.DeltaAction = p.Action
		event.DeltaUsers = p.Users
		event.NodeID = p.NodeID

	case WSEventSyncDevices:
		nlog.Core().Debug("ws sync devices event received")
		var p syncDevicesPayload
		if err := decodeData(msg.Data, &p); err != nil {
			nlog.Core().Warn("ws: cannot decode devices payload", "error", err)
			return
		}
		users, err := decodeDeviceUsers(p.Users)
		if err != nil {
			nlog.Core().Warn("ws: cannot decode devices payload", "error", err)
			return
		}
		event.DeviceUsers = users
		event.NodeID = p.NodeID

	case WSEventSyncNodes:
		nlog.Core().Info("ws sync nodes event received (machine node list changed)")
		var p syncNodesPayload
		if err := decodeData(msg.Data, &p); err != nil {
			nlog.Core().Warn("ws: cannot decode nodes payload", "error", err)
			return
		}
		event.Nodes = p.Nodes
	}

	w.onEvent(event)
}

// SendDeviceReport sends local device snapshot to panel via WS.
func (w *WSClient) SendDeviceReport(devices map[int][]string) {
	w.SendDeviceReportForNode(0, devices)
}

// SendDeviceReportForNode sends a device report tagged with a specific node_id.
// nodeID == 0 omits the field (legacy single-node mode).
func (w *WSClient) SendDeviceReportForNode(nodeID int, devices map[int][]string) {
	if !w.connected.Load() {
		return
	}

	var payload interface{}
	if nodeID > 0 {
		payload = map[string]interface{}{"node_id": nodeID, "devices": devices}
	} else {
		payload = devices
	}

	d, err := json.Marshal(payload)
	if err != nil {
		return
	}
	msg := wsMessage{
		Event:     WSEventReportDevices,
		Data:      d,
		Timestamp: time.Now().Unix(),
	}

	w.tryWrite(msg, "ws write channel full, skipping device report")
}

// SendNodeStatus sends a node.status event with the given node_id (machine mode).
func (w *WSClient) SendNodeStatus(nodeID int, stats map[string]interface{}) {
	if !w.connected.Load() || stats == nil {
		return
	}
	if nodeID > 0 {
		stats["node_id"] = nodeID
	}
	data, _ := json.Marshal(stats)
	msg := wsMessage{
		Event:     "node.status",
		Data:      data,
		Timestamp: time.Now().Unix(),
	}
	w.tryWrite(msg, "ws write channel full, skipping node status")
}

// SendRaw sends a raw message to the panel via WebSocket.
func (w *WSClient) SendRaw(event string, data json.RawMessage) {
	if !w.connected.Load() {
		return
	}

	msg := wsMessage{
		Event:     event,
		Data:      data,
		Timestamp: time.Now().Unix(),
	}

	w.tryWrite(msg, "ws write channel full, skipping raw message", "event", event)
}

func (w *WSClient) tryWrite(msg wsMessage, warnMsg string, args ...any) {
	chPtr := w.writeCh.Load()
	if chPtr == nil {
		return
	}
	select {
	case *chPtr <- msg:
	default:
		nlog.Core().Warn(warnMsg, args...)
	}
}
