package nlog

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

func TestInitAndLevels(t *testing.T) {
	buf := &bytes.Buffer{}
	prev := Init(buf, slog.LevelWarn, false)
	defer func() {
		Init(prev, slog.LevelInfo, true)
	}()

	nl := Core()
	nl.Debug("should not appear")
	nl.Info("should not appear either")
	nl.Warn("warning message", "code", 123)
	nl.Error("error message")

	out := buf.String()
	if strings.Contains(out, "should not appear") {
		t.Fatalf("debug/info message appeared when minLevel was Warn: %s", out)
	}
	if !strings.Contains(out, "warning message code=123") {
		t.Fatalf("warn message missing or formatted wrong: %s", out)
	}
	if !strings.Contains(out, "error message") {
		t.Fatalf("error message missing: %s", out)
	}
}

func TestColorOutput(t *testing.T) {
	buf := &bytes.Buffer{}
	Init(buf, slog.LevelInfo, true)

	nl := ForNode("shadowsocks", 10005)
	nl.Info("node started")

	out := buf.String()
	if !strings.Contains(out, ColorCyan) {
		t.Fatalf("expected ANSI color code in output: %q", out)
	}
	if !strings.Contains(out, "shadowsocks:10005") {
		t.Fatalf("expected node prefix in output: %q", out)
	}
}

func TestContextLogger(t *testing.T) {
	ctx := context.Background()
	defaultL := FromContext(ctx)
	if defaultL.prefix != "core" {
		t.Fatalf("expected core logger for empty context, got: %s", defaultL.prefix)
	}

	nodeL := ForNode("vmess", 443)
	ctxWithNode := WithNode(ctx, nodeL)
	gotL := FromContext(ctxWithNode)
	if gotL.prefix != "vmess:443" {
		t.Fatalf("expected vmess:443 logger, got: %s", gotL.prefix)
	}
}

func TestStartupSummary(t *testing.T) {
	buf := &bytes.Buffer{}
	Init(buf, slog.LevelInfo, false)

	s := NewStartupSummary()
	s.AddNode("ss", 1001, 10)
	s.AddNode("ss", 1002, 20)
	s.AddNode("trojan", 1003, 30)
	s.Print()

	out := buf.String()
	if !strings.Contains(out, "started 3 nodes") {
		t.Fatalf("expected 3 nodes in summary: %s", out)
	}
	if !strings.Contains(out, "shadowsocks:1001,1002") {
		t.Fatalf("expected grouped shadowsocks nodes: %s", out)
	}
	if !strings.Contains(out, "trojan:1003") {
		t.Fatalf("expected trojan node: %s", out)
	}
}

func TestConcurrentLogging(t *testing.T) {
	buf := &bytes.Buffer{}
	Init(buf, slog.LevelInfo, false)

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			nl := ForNode("vless", 8000+id)
			nl.Info("worker message", "id", id)
		}(i)
	}
	wg.Wait()

	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 20 {
		t.Fatalf("expected at least 20 log lines, got %d", len(lines))
	}
}
