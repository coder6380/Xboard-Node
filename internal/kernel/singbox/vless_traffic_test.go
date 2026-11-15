package singbox

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/kernel"
	"github.com/cedar2025/xboard-node/internal/model"
)

// TestVLESSRealTraffic 验证真实 VLESS 连接的双向数据进入内核上报接口。
func TestVLESSRealTraffic(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	portProbe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := portProbe.Addr().(*net.TCPAddr).Port
	portProbe.Close()
	user := model.UserSpec{ID: 123, UUID: "11111111-1111-1111-1111-111111111111"}
	// 使用标准库的测试证书覆盖问题中的 TLS + WebSocket 路径。
	certServer := httptest.NewTLSServer(nil)
	cert := certServer.TLS.Certificates[0]
	certServer.Close()
	key, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	tlsCert := kernel.TLSCert{CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}), KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})}
	k := New(config.KernelConfig{Type: "sing-box"})
	if err := k.Start(&model.NodeSpec{Protocol: "vless", ListenIP: "127.0.0.1", ServerPort: port, TLS: 1, Network: "ws", NetworkSettings: map[string]any{"path": "/ws", "headers": map[string]any{"Host": "example.com"}}, CustomRouteRules: []model.CustomRouteRule{{Match: model.RouteMatch{IPCIDRs: []string{"127.0.0.1/32"}}, Action: model.RouteAction{Type: "direct"}}}}, []model.UserSpec{user}, tlsCert); err != nil {
		t.Fatal(err)
	}
	defer k.Stop()
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(tlsCert.CertPEM)
	dialer := websocket.Dialer{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: "example.com"}}
	conn, _, err := dialer.Dial("wss://"+net.JoinHostPort("127.0.0.1", itoaPort(port))+"/ws", http.Header{"Host": []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	// 直接编码 VLESS 请求，避免测试客户端借用被测内核的统计实现。
	header := append([]byte{0}, bytes.Repeat([]byte{0x11}, 16)...)
	header = append(header, 0, 1, 0, 0, 1, 127, 0, 0, 1)
	binary.BigEndian.PutUint16(header[19:21], uint16(target.Addr().(*net.TCPAddr).Port))
	payload := bytes.Repeat([]byte("vless-traffic"), 100)
	if err := conn.WriteMessage(websocket.BinaryMessage, append(header, payload...)); err != nil {
		t.Fatal(err)
	}
	var response []byte
	for len(response) < 2+len(payload) {
		_, chunk, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		response = append(response, chunk...)
	}
	if !bytes.Equal(response[2:], payload) {
		t.Fatal("代理返回内容不一致")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		traffic, _, _, err := k.GetUserTraffic(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got := traffic[user.ID]; got[0] >= int64(len(payload)) && got[1] >= int64(len(payload)) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("实际收发 %d 字节，上报流量 = %v", len(payload), traffic[user.ID])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// itoaPort 返回 TCP 端口的十进制字符串。
func itoaPort(port int) string { return fmt.Sprint(port) }
