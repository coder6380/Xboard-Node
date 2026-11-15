package singbox

import (
	"context"
	"testing"

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
