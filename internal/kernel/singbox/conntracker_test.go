package singbox

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	N "github.com/sagernet/sing/common/network"
	"golang.org/x/time/rate"
)

// TestPacketUnwrapTrafficDirection 验证快速路径的计数方向与普通读写一致。
func TestPacketUnwrapTrafficDirection(t *testing.T) {
	stats := &userStats{}
	conn := &trackedPacketConn{us: stats}
	_, readers := conn.UnwrapPacketReader()
	_, writers := conn.UnwrapPacketWriter()
	if len(readers) != 1 || len(writers) != 1 {
		t.Fatalf("unexpected callback counts: readers=%d writers=%d", len(readers), len(writers))
	}
	readers[0](13)
	writers[0](29)
	if got := stats.upload.Load(); got != 13 {
		t.Errorf("upload = %d, want 13", got)
	}
	if got := stats.download.Load(); got != 29 {
		t.Errorf("download = %d, want 29", got)
	}
}

// TestPacketFastPathLimit 验证快速路径保留双向计数与令牌桶限制。
func TestPacketFastPathLimit(t *testing.T) {
	for _, direction := range []string{"read", "write"} {
		t.Run(direction, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			lim := rate.NewLimiter(1, 1)
			stats := &userStats{}
			base := &testPacketCloser{}
			conn := &trackedPacketConn{PacketConn: base, us: stats, limiter: lim, ctx: ctx}
			var counts []N.CountFunc
			if direction == "read" {
				_, counts = N.UnwrapCountPacketReader(conn, nil)
			} else {
				_, counts = N.UnwrapCountPacketWriter(conn, nil)
			}
			if len(counts) != 1 {
				t.Fatalf("回调数量 = %d，期望 1", len(counts))
			}
			counts[0](1)
			cancel()
			counts[0](1)
			if lim.Tokens() >= 0.5 {
				t.Fatal("限速令牌未消耗")
			}
			if direction == "read" && stats.upload.Load() != 2 || direction == "write" && stats.download.Load() != 2 {
				t.Fatalf("计数方向错误：上传=%d 下载=%d", stats.upload.Load(), stats.download.Load())
			}
		})
	}
}

// testPacketCloser 用于观察主动关闭是否到达底层连接。
type testPacketCloser struct {
	N.PacketConn
	closed bool
}

// Close 记录底层连接的关闭状态。
func (c *testPacketCloser) Close() error { c.closed = true; return nil }

// TestCloseByUUID 验证仅关闭目标用户的 TCP 和 UDP 并清理在线计数。
func TestCloseByUUID(t *testing.T) {
	tracker := NewConnTracker(0)
	tracker.SetUserMap(map[string]int{"target": 1, "other": 2})
	tcp := &testConn{}
	udp := &testPacketCloser{}
	other := &testPacketCloser{}
	tracker.RoutedConnection(context.Background(), tcp, testInboundContext("target", "1.1.1.1"), nil, nil)
	tracker.RoutedPacketConnection(context.Background(), udp, testInboundContext("target", "1.1.1.2"), nil, nil)
	otherConn := tracker.RoutedPacketConnection(context.Background(), other, testInboundContext("other", "1.1.1.3"), nil, nil)
	if n := tracker.CloseByUUID("target"); n != 2 || !tcp.closed || !udp.closed || other.closed {
		t.Fatalf("关闭结果异常：数量=%d TCP=%v UDP=%v 其他=%v", n, tcp.closed, udp.closed, other.closed)
	}
	if n := tracker.ActiveCount(); n != 1 {
		t.Fatalf("在线连接数 = %d，期望 1", n)
	}
	if n := tracker.CloseByUUID("target"); n != 0 {
		t.Fatalf("重复关闭数量 = %d，期望 0", n)
	}
	_ = otherConn.Close()
}

// TestMakeCountFuncExceedingBurstDoesNotHang 验证 n > limiter.Burst() 分片消费而不会触发 InfDuration 永久卡死。
func TestMakeCountFuncExceedingBurstDoesNotHang(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	// 10000 bytes/sec with tiny burst=100; transferring 350 bytes (> burst) must finish in ~25-40ms.
	lim := rate.NewLimiter(10000, 100)
	stats := &userStats{}
	conn := &trackedConn{us: stats, limiter: lim, ctx: ctx}
	fn := conn.makeCountFunc(&stats.download)

	done := make(chan struct{})
	go func() {
		fn(350)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("makeCountFunc hung when n > limiter.Burst()")
	}
	if got := stats.download.Load(); got != 350 {
		t.Fatalf("download = %d, want 350", got)
	}
}

// TestNaiveNumericUserIdentityTracking 验证 naive 协议使用数字用户 ID 作为 username 时，
// 流量统计、设备限制与按 UUID 强制断连均正常工作。
func TestNaiveNumericUserIdentityTracking(t *testing.T) {
	tracker := NewConnTracker(0)
	uuid := "aaaaaaaa-1111-2222-3333-444444444444"
	tracker.SetUserMap(map[string]int{uuid: 42})
	tracker.SetDeviceLimitFunc(func(u string) (int, bool) {
		if u == uuid {
			return 1, true
		}
		return 0, false
	})

	conn1 := &testConn{}
	wrapped1 := tracker.RoutedConnection(context.Background(), conn1, testInboundContext("42", "1.1.1.1"), nil, nil)
	if conn1.closed {
		t.Fatal("first connection for naive user should be allowed")
	}

	// Second IP must be rejected by device limit=1 (resolved via numeric ID -> UUID)
	conn2 := &testConn{}
	tracker.RoutedConnection(context.Background(), conn2, testInboundContext("42", "2.2.2.2"), nil, nil)
	if !conn2.closed {
		t.Fatal("second IP for naive user should be rejected by device limit")
	}

	_, aliveIPs, connCount := tracker.GetUserTraffic()
	if connCount != 1 || !aliveIPs[42]["1.1.1.1"] {
		t.Fatalf("unexpected naive tracking state: connCount=%d alive=%v", connCount, aliveIPs)
	}

	// CloseByUUID must close the naive connection registered under numeric username "42"
	if n := tracker.CloseByUUID(uuid); n != 1 || !conn1.closed {
		t.Fatalf("CloseByUUID(%s) = %d, closed=%v; want 1, true", uuid, n, conn1.closed)
	}
	_ = wrapped1
}

func TestConnTracker_ConcurrentAdmitAndTrackConnAtomic(t *testing.T) {
	tracker := NewConnTracker(0)
	uuid := "bbbbbbbb-1111-2222-3333-444444444444"
	tracker.SetUserMap(map[string]int{uuid: 77})
	tracker.SetDeviceLimitFunc(func(u string) (int, bool) {
		if u == uuid {
			return 1, true
		}
		return 0, false
	})

	// Occupy the single slot with the lowest IP.
	conn0 := &testConn{}
	tracker.RoutedConnection(context.Background(), conn0, testInboundContext(uuid, "10.0.0.1"), nil, nil)
	if conn0.closed {
		t.Fatal("first connection should be allowed")
	}

	var wg sync.WaitGroup
	var admitted atomic.Int32
	for g := 0; g < 16; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ip := "10.0.0." + string(rune('2'+id%8))
			for i := 0; i < 50; i++ {
				c := &testConn{}
				tracker.RoutedConnection(context.Background(), c, testInboundContext(uuid, ip), nil, nil)
				if !c.closed {
					admitted.Add(1)
				}
			}
		}(g)
	}
	wg.Wait()

	if admitted.Load() != 0 {
		t.Fatalf("device limit bypassed %d times under concurrency", admitted.Load())
	}
}
