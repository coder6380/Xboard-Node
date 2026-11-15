package machine

import (
	"context"
	"testing"

	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/controlplane"
	"github.com/cedar2025/xboard-node/internal/panel"
)

func TestFinishNodeAllowsRediscovery(t *testing.T) {
	o := New(&config.Config{Machine: &config.MachineConfig{}})
	ctx, cancel := context.WithCancel(context.Background())
	h := &nodeHandle{cancel: cancel, done: make(chan struct{}), mailbox: controlplane.NewNodeMailbox()}
	o.nodes[1] = h
	o.mailboxes[1] = h.mailbox
	o.statuses[1] = make(chan controlplane.StatusChange, 1)

	o.finishNode(1, h)
	if _, ok := o.nodes[1]; ok {
		t.Fatal("exited node remains registered")
	}
	if _, ok := o.mailboxes[1]; ok {
		t.Fatal("exited node mailbox remains registered")
	}
	if _, ok := o.statuses[1]; ok {
		t.Fatal("exited node status remains registered")
	}
	select {
	case <-h.done:
	default:
		t.Fatal("node completion was not signaled")
	}
	if ctx.Err() == nil {
		t.Fatal("node context was not canceled")
	}
}

func TestFinishOldNodePreservesNewNode(t *testing.T) {
	o := New(&config.Config{Machine: &config.MachineConfig{}})
	old := &nodeHandle{cancel: func() {}, done: make(chan struct{})}
	current := &nodeHandle{cancel: func() {}, done: make(chan struct{}), mailbox: controlplane.NewNodeMailbox()}
	o.nodes[1] = current
	o.mailboxes[1] = current.mailbox
	status := make(chan controlplane.StatusChange, 1)
	o.statuses[1] = status

	o.finishNode(1, old)
	if o.nodes[1] != current || o.mailboxes[1] != current.mailbox || o.statuses[1] != status {
		t.Fatal("old node cleanup removed current node state")
	}
}

func TestSyncNodesEventsCoalesce(t *testing.T) {
	o := New(&config.Config{Machine: &config.MachineConfig{}})
	for i := 0; i < 100; i++ {
		o.onWSEvent(panel.WSEvent{Type: panel.WSEventSyncNodes})
	}
	if got := len(o.discoveryCh); got != 1 {
		t.Fatalf("expected one queued discovery, got %d", got)
	}
}

func TestStartNodeAfterShutdownOrCancel(t *testing.T) {
	o := New(&config.Config{Machine: &config.MachineConfig{}})
	o.stopAll()
	o.startNode(context.Background(), panel.MachineNode{ID: 1})
	if len(o.nodes) != 0 {
		t.Fatal("started a node after shutdown")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	o = New(&config.Config{Machine: &config.MachineConfig{}})
	o.startNode(ctx, panel.MachineNode{ID: 2})
	if len(o.nodes) != 0 {
		t.Fatal("started a node after cancellation")
	}
}
