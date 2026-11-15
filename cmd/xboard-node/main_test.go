package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

// TestSignalLifecycleDuringReload 验证重载取消旧周期后信号仍能启动退出保护。
func TestSignalLifecycleDuringReload(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	lifecycle, stopLifecycle := context.WithCancel(context.Background())
	defer stopLifecycle()
	signals := make(chan os.Signal, 2)
	exits := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleShutdownSignals(lifecycle, cancel, signals, 100*time.Millisecond, func(code int) { exits <- code })
	}()
	// 模拟重载时已取消的服务周期，不能影响进程级信号处理。
	cycle, stopCycle := context.WithCancel(ctx)
	stopCycle()
	<-cycle.Done()
	signals <- syscall.SIGTERM
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("重载等待期间信号未取消进程上下文")
	}
	signals <- syscall.SIGTERM
	select {
	case code := <-exits:
		if code != 1 {
			t.Fatalf("二次信号退出码 = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("二次信号未强制退出")
	}
	<-done
}

// TestSignalWatchdogRunsDuringBlockedCancel 验证取消阻塞时退出计时仍然生效。
func TestSignalWatchdogRunsDuringBlockedCancel(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	exits := make(chan int, 1)
	release := make(chan struct{})
	defer close(release)
	go handleShutdownSignals(ctx, func() { <-release }, signals, 20*time.Millisecond, func(code int) { exits <- code })
	select {
	case code := <-exits:
		if code != 2 {
			t.Fatalf("超时退出码 = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("取消回调阻塞导致退出计时失效")
	}
}

// TestSignalShutdownTimeout 验证首次信号后关闭超时仍会强制退出。
func TestSignalShutdownTimeout(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM
	exits := make(chan int, 1)
	go handleShutdownSignals(ctx, func() {}, signals, 20*time.Millisecond, func(code int) { exits <- code })
	select {
	case code := <-exits:
		if code != 2 {
			t.Fatalf("超时退出码 = %d", code)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭超时未强制退出")
	}
}
