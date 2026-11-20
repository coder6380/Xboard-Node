package controlplane

import (
	"testing"

	"github.com/cedar2025/xboard-node/internal/model"
)

func TestNodeMailboxLatestStateAndNotifyCollapse(t *testing.T) {
	mb := NewNodeMailbox()
	cfg1 := &model.NodeSpec{Protocol: "vmess", ServerPort: 10010}
	cfg2 := &model.NodeSpec{Protocol: "vless", ServerPort: 10020}

	mb.Apply(Event{Type: EventSyncConfig, Config: cfg1})
	mb.Apply(Event{Type: EventSyncConfig, Config: cfg2})

	select {
	case <-mb.NotifyCh():
	default:
		t.Fatal("expected collapsed notify signal")
	}

	mb.MarkReady()
	state := mb.DrainIfReady()
	if !state.HasConfig || state.Config == nil {
		t.Fatal("expected config state")
	}
	if state.Config.Protocol != "vless" || state.Config.ServerPort != 10020 {
		t.Fatalf("unexpected config: %+v", state.Config)
	}
}

func TestNodeMailboxFullUsersOverwriteAndDeltaBeforeBaseline(t *testing.T) {
	mb := NewNodeMailbox()
	mb.Apply(Event{
		Type:        EventSyncUserDelta,
		DeltaAction: "add",
		DeltaUsers:  []model.UserSpec{{ID: 1, UUID: "u1"}},
	})
	mb.MarkReady()
	state := mb.DrainIfReady()
	if !state.NeedsReconcile {
		t.Fatal("expected reconcile when delta arrives before baseline")
	}

	mb.Apply(Event{Type: EventSyncUsers, Users: []model.UserSpec{{ID: 1, UUID: "u1"}}})
	mb.Apply(Event{Type: EventSyncUsers, Users: []model.UserSpec{{ID: 2, UUID: "u2"}}})
	state = mb.DrainIfReady()
	if !state.HasUsers {
		t.Fatal("expected users snapshot")
	}
	if len(state.Users) != 1 || state.Users[0].ID != 2 {
		t.Fatalf("unexpected users: %+v", state.Users)
	}
}

// TestNodeMailboxEmptyUserSnapshots 验证空全量快照和删除最后一名用户均可撤销用户。
func TestNodeMailboxEmptyUserSnapshots(t *testing.T) {
	for _, tt := range []struct {
		name  string
		event Event
	}{
		{"full", Event{Type: EventSyncUsers, Users: []model.UserSpec{}}},
		{"remove last", Event{Type: EventSyncUserDelta, DeltaAction: "remove", DeltaUsers: []model.UserSpec{{ID: 1}}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mb := NewNodeMailbox()
			mb.SeedBaseline([]model.UserSpec{{ID: 1}}, nil)
			mb.MarkReady()
			mb.Apply(tt.event)
			state := mb.DrainIfReady()
			if !state.HasUsers || state.Users == nil || len(state.Users) != 0 || state.NeedsReconcile {
				t.Fatalf("expected non-nil empty users snapshot, got %+v", state)
			}
		})
	}
}

// TestNodeMailboxNilUserSnapshot 验证缺失用户数据不会覆盖已有快照。
func TestNodeMailboxNilUserSnapshot(t *testing.T) {
	mb := NewNodeMailbox()
	mb.MarkReady()
	mb.Apply(Event{Type: EventSyncUsers, Users: []model.UserSpec{{ID: 1}}})
	mb.Apply(Event{Type: EventSyncUsers, Users: nil})
	state := mb.DrainIfReady()
	if !state.HasUsers || len(state.Users) != 1 || state.Users[0].ID != 1 {
		t.Fatalf("nil snapshot replaced users: %+v", state)
	}
}

// TestNodeMailboxStaleRemovalPreservesReplacement 验证旧 UUID 的删除不会覆盖新基线。
func TestNodeMailboxStaleRemovalPreservesReplacement(t *testing.T) {
	mb := NewNodeMailbox()
	mb.SeedBaseline([]model.UserSpec{{ID: 1, UUID: "old"}}, nil)
	mb.Apply(Event{Type: EventSyncUserDelta, DeltaAction: "add", DeltaUsers: []model.UserSpec{{ID: 1, UUID: "new"}}})
	mb.Apply(Event{Type: EventSyncUserDelta, DeltaAction: "remove", DeltaUsers: []model.UserSpec{{ID: 1, UUID: "old"}}})
	mb.MarkReady()
	state := mb.DrainIfReady()
	if !state.HasUsers || len(state.Users) != 1 || state.Users[0].UUID != "new" {
		t.Fatalf("replacement lost: %+v", state)
	}
}

func TestNodeMailboxDevicesLatestWins(t *testing.T) {
	mb := NewNodeMailbox()
	mb.Apply(Event{Type: EventSyncDevices, DeviceUsers: map[int][]string{1: {"1.1.1.1"}}})
	mb.Apply(Event{Type: EventSyncDevices, DeviceUsers: map[int][]string{1: {"2.2.2.2"}}})
	mb.MarkReady()
	state := mb.DrainIfReady()
	if !state.HasDevices {
		t.Fatal("expected devices state")
	}
	if got := state.DeviceUsers[1][0]; got != "2.2.2.2" {
		t.Fatalf("unexpected device state: %v", state.DeviceUsers)
	}
}

func TestNodeMailboxReconcileNotifies(t *testing.T) {
	mb := NewNodeMailbox()
	mb.MarkReady()

	// Drain the MarkReady notification
	select {
	case <-mb.NotifyCh():
	default:
	}

	// Delta without baseline should trigger reconcile AND notify
	mb.Apply(Event{
		Type:        EventSyncUserDelta,
		DeltaAction: "add",
		DeltaUsers:  []model.UserSpec{{ID: 1, UUID: "u1"}},
	})

	select {
	case <-mb.NotifyCh():
	default:
		t.Fatal("expected notify on reconcile path")
	}

	state := mb.DrainIfReady()
	if !state.NeedsReconcile {
		t.Fatal("expected NeedsReconcile")
	}
}

func TestNodeMailboxSeedBaselineEnablesDelta(t *testing.T) {
	mb := NewNodeMailbox()
	mb.SeedBaseline([]model.UserSpec{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}, nil)
	mb.MarkReady()

	// Drain MarkReady notify
	select {
	case <-mb.NotifyCh():
	default:
	}

	// Delta should now apply incrementally, not trigger reconcile
	mb.Apply(Event{
		Type:        EventSyncUserDelta,
		DeltaAction: "add",
		DeltaUsers:  []model.UserSpec{{ID: 3, UUID: "u3"}},
	})

	select {
	case <-mb.NotifyCh():
	default:
		t.Fatal("expected notify after delta")
	}

	state := mb.DrainIfReady()
	if state.NeedsReconcile {
		t.Fatal("should not need reconcile after seeded baseline")
	}
	if !state.HasUsers {
		t.Fatal("expected users")
	}
	if len(state.Users) != 3 {
		t.Fatalf("expected 3 users, got %d: %+v", len(state.Users), state.Users)
	}
}

func TestNodeMailboxDeltaFailedApplyNotifies(t *testing.T) {
	mb := NewNodeMailbox()
	mb.SeedBaseline([]model.UserSpec{{ID: 1, UUID: "u1"}}, nil)
	mb.MarkReady()

	// Drain MarkReady notify
	select {
	case <-mb.NotifyCh():
	default:
	}

	// Invalid action should trigger reconcile and still notify
	mb.Apply(Event{
		Type:        EventSyncUserDelta,
		DeltaAction: "invalid_action",
		DeltaUsers:  []model.UserSpec{{ID: 2, UUID: "u2"}},
	})

	select {
	case <-mb.NotifyCh():
	default:
		t.Fatal("expected notify on failed delta apply")
	}

	state := mb.DrainIfReady()
	if !state.NeedsReconcile {
		t.Fatal("expected NeedsReconcile for invalid delta action")
	}
}

func TestNodeMailboxResetAndSyncUsersPreventsResurrection(t *testing.T) {
	mb := NewNodeMailbox()
	mb.SeedBaseline([]model.UserSpec{{ID: 1, UUID: "u1"}, {ID: 2, UUID: "u2"}}, nil)
	mb.MarkReady()

	// WS disconnect invalidates baseline; a delta before REST resync must request reconcile.
	mb.ResetUserBaseline()
	mb.Apply(Event{
		Type:        EventSyncUserDelta,
		DeltaAction: "add",
		DeltaUsers:  []model.UserSpec{{ID: 4, UUID: "u4"}},
	})
	st := mb.DrainIfReady()
	if !st.NeedsReconcile || st.HasUsers {
		t.Fatalf("expected NeedsReconcile without resurrecting stale users, got %+v", st)
	}

	// REST poll updates live users to [u2, u3] and syncs mailbox baseline.
	mb.SyncUsers([]model.UserSpec{{ID: 2, UUID: "u2"}, {ID: 3, UUID: "u3"}})
	mb.Apply(Event{
		Type:        EventSyncUserDelta,
		DeltaAction: "add",
		DeltaUsers:  []model.UserSpec{{ID: 4, UUID: "u4"}},
	})
	st = mb.DrainIfReady()
	if st.NeedsReconcile || !st.HasUsers {
		t.Fatalf("expected clean delta apply after SyncUsers, got %+v", st)
	}
	if len(st.Users) != 3 {
		t.Fatalf("expected [u2, u3, u4], got %+v", st.Users)
	}
	for _, u := range st.Users {
		if u.ID == 1 {
			t.Fatalf("deleted user 1 was resurrected: %+v", st.Users)
		}
	}
}
