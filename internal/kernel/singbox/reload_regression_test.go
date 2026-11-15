package singbox

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/service"

	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/kernel"
	"github.com/cedar2025/xboard-node/internal/model"
)

// reloadTestInbound 模拟不支持用户热更新的入站。
type reloadTestInbound struct{ adapter.Inbound }

func (reloadTestInbound) Type() string { return "vmess" }
func (reloadTestInbound) Tag() string  { return "vmess-in" }

// reloadWrongInbound 模拟接口用户类型与入站选项不一致的入站。
type reloadWrongInbound struct{ reloadTestInbound }

func (reloadWrongInbound) UpdateUsers([]option.VLESSUser) error { return nil }

// reloadTestManager 记录入站重建所使用的选项。
type reloadTestManager struct {
	adapter.InboundManager
	inbound   adapter.Inbound
	created   any
	createErr error
}

func (m *reloadTestManager) Get(string) (adapter.Inbound, bool) { return m.inbound, m.inbound != nil }
func (m *reloadTestManager) Remove(string) error                { m.inbound = nil; return nil }
func (m *reloadTestManager) Create(_ context.Context, _ adapter.Router, _ log.ContextLogger, _ string, _ string, options any) error {
	m.created = options
	return m.createErr
}

// reloadTestRouter 可注入路由更新失败。
type reloadTestRouter struct {
	adapter.Router
	err    error
	called bool
}

func (r *reloadTestRouter) UpdateRules([]option.Rule, []option.RuleSet) error {
	r.called = true
	return r.err
}

// newReloadTestBox 构造可检查用户状态和入站重建行为的内核。
func newReloadTestBox(inbound adapter.Inbound) (*SingBox, *reloadTestManager, *reloadTestRouter) {
	manager := &reloadTestManager{inbound: inbound}
	router := &reloadTestRouter{}
	ctx := include.Context(context.Background())
	ctx = service.ContextWith[adapter.InboundManager](ctx, manager)
	ctx = service.ContextWith[adapter.Router](ctx, router)
	users := []model.UserSpec{{ID: 1, UUID: "aaaaaaaa-1111-2222-3333-444444444444"}}
	tracker := NewConnTracker(0)
	tracker.SetUserMap(buildUserMap(users))
	return &SingBox{cfg: config.KernelConfig{}, box: &box.Box{}, ctx: ctx, nodeConfig: &model.NodeSpec{Protocol: "vmess"}, users: users, connTracker: tracker}, manager, router
}

func TestReloadUsersRecreatesUnmatchedInbound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inbound adapter.Inbound
	}{
		{"unsupported interface", reloadTestInbound{}},
		{"mismatched options", reloadWrongInbound{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, manager, _ := newReloadTestBox(tc.inbound)
			newUser := model.UserSpec{ID: 2, UUID: "bbbbbbbb-5555-6666-7777-888888888888"}
			count, err := s.AddUsers([]model.UserSpec{newUser})
			if err != nil || count != 1 {
				t.Fatalf("AddUsers() = %d, %v", count, err)
			}
			opts, ok := manager.created.(*option.VMessInboundOptions)
			if !ok || len(opts.Users) != 2 || opts.Users[1].UUID != newUser.UUID {
				t.Fatalf("recreated options = %#v", manager.created)
			}
			if len(s.users) != 2 || s.connTracker.uuidMap[newUser.UUID] != newUser.ID {
				t.Fatalf("user state not updated: users=%v tracker=%v", s.users, s.connTracker.uuidMap)
			}
		})
	}
}

func TestReloadRecreatesUnmatchedInbound(t *testing.T) {
	s, manager, _ := newReloadTestBox(reloadTestInbound{})
	if err := s.Reload(s.nodeConfig, s.users, kernel.TLSCert{}); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if _, ok := manager.created.(*option.VMessInboundOptions); !ok {
		t.Fatalf("recreated options = %#v", manager.created)
	}
}

func TestReloadUsersRecreateFailurePreservesState(t *testing.T) {
	s, manager, _ := newReloadTestBox(reloadTestInbound{})
	manager.createErr = errors.New("create failed")
	oldUsers := append([]model.UserSpec(nil), s.users...)
	newUser := model.UserSpec{ID: 2, UUID: "bbbbbbbb-5555-6666-7777-888888888888"}
	if count, err := s.AddUsers([]model.UserSpec{newUser}); err == nil || count != 0 {
		t.Fatalf("AddUsers() = %d, %v", count, err)
	}
	if !reflect.DeepEqual(s.users, oldUsers) || !reflect.DeepEqual(s.connTracker.uuidMap, buildUserMap(oldUsers)) {
		t.Fatalf("state changed on failure: users=%v tracker=%v", s.users, s.connTracker.uuidMap)
	}
}

func TestReloadRejectsOutboundChangeAndRoutingFailure(t *testing.T) {
	t.Run("outbound change", func(t *testing.T) {
		s, manager, router := newReloadTestBox(reloadTestInbound{})
		node := *s.nodeConfig
		node.CustomOutbounds = []model.OutboundConfig{{Tag: "custom", Protocol: "direct"}}
		if err := s.Reload(&node, s.users, kernel.TLSCert{}); err == nil || !strings.Contains(err.Error(), "restart required") {
			t.Fatalf("Reload() error = %v", err)
		}
		if router.called || manager.created != nil || s.nodeConfig == &node {
			t.Fatal("outbound change modified live state")
		}
	})
	t.Run("routing failure", func(t *testing.T) {
		s, manager, router := newReloadTestBox(reloadTestInbound{})
		router.err = errors.New("route failed")
		node := *s.nodeConfig
		if err := s.Reload(&node, s.users, kernel.TLSCert{}); err == nil || !strings.Contains(err.Error(), "route failed") {
			t.Fatalf("Reload() error = %v", err)
		}
		if !router.called || manager.created != nil || s.nodeConfig == &node {
			t.Fatal("routing failure modified inbound or saved state")
		}
	})
}
