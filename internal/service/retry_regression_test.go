package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/controlplane"
	"github.com/cedar2025/xboard-node/internal/kernel"
	"github.com/cedar2025/xboard-node/internal/model"
)

// retryKernel 模拟配置重载及备用启动失败。
type retryKernel struct {
	*fakeKernel
	reloadCalls   int
	reloadErr     error
	stopCalls     int
	portHeld      bool
	stopKeepsPort bool
}

// Stop 模拟释放旧实例占用的端口。
func (k *retryKernel) Stop() {
	k.stopCalls++
	if !k.stopKeepsPort {
		k.portHeld = false
	}
	k.fakeKernel.Stop()
}

// Start 在旧实例仍占用端口时模拟启动失败。
func (k *retryKernel) Start(nc *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	if k.portHeld && k.startErr == nil {
		k.startCalls++
		return errors.New("port already in use")
	}
	return k.fakeKernel.Start(nc, users, tls)
}

// Reload 记录重载次数并返回预设错误。
func (k *retryKernel) Reload(_ *model.NodeSpec, _ []model.UserSpec, _ kernel.TLSCert) error {
	k.reloadCalls++
	return k.reloadErr
}

// TestStaleUserRemovalDoesNotDeleteReplacement 验证旧 UUID 删除事件不会删除同 ID 的新用户。
func TestStaleUserRemovalDoesNotDeleteReplacement(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "new"}})
	s.applyUserDelta(context.Background(), "remove", []model.UserSpec{{ID: 1, UUID: "old"}})
	if k.removeCalls != 0 || len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "new" {
		t.Fatalf("stale removal changed users: calls=%d users=%v", k.removeCalls, s.lastUsers)
	}
}

// TestFailedCombinedPullRetriesUsers 验证配置和用户同时应用失败后用户不会被错误去重。
func TestFailedCombinedPullRetriesUsers(t *testing.T) {
	k := &retryKernel{fakeKernel: &fakeKernel{running: true, startErr: errors.New("start failed")}, reloadErr: errors.New("reload failed")}
	s := newTestService(k.fakeKernel)
	s.kernel = k
	s.cfg = &config.Config{}
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 1000}
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "old"}})
	next := &model.NodeSpec{Protocol: "vless", ServerPort: 2000}
	users := []model.UserSpec{{ID: 2, UUID: "new"}}
	result := pullResult{config: next, configHash: computeConfigHash(next), users: users, userHash: computeUserHash(users)}
	s.applyPullResult(context.Background(), result)
	if s.lastUserHash == result.userHash {
		t.Fatal("failed combined update marked users as applied")
	}
	k.reloadErr, k.startErr = nil, nil
	s.applyPullResult(context.Background(), result)
	if s.lastUserHash != result.userHash || len(s.appliedState.Users) != 1 || s.appliedState.Users[0].UUID != "new" {
		t.Fatalf("retry did not apply users: %+v", s.appliedState.Users)
	}
}

// retrySource 模拟首次拉取失败、随后恢复的控制面。
type retrySource struct {
	controlplane.Source
	calls chan int
}

// SupportsPolling 表示该控制面支持 REST 拉取。
func (r *retrySource) SupportsPolling() bool { return true }

// Poll 模拟临时错误并允许测试观察重试。
func (r *retrySource) Poll(context.Context) (controlplane.Snapshot, error) {
	select {
	case r.calls <- 1:
	default:
	}
	return controlplane.Snapshot{}, errors.New("temporary failure")
}

// TestConnectedPendingReconcileRetries 验证 WS 保持连接时失败的协调仍会定时重试。
func TestConnectedPendingReconcileRetries(t *testing.T) {
	s := newTestService(&fakeKernel{})
	s.source = &retrySource{calls: make(chan int, 2)}
	s.pullResults = make(chan pullResult, 1)
	s.wsResyncPending.Store(true)
	s.pollTick(context.Background(), true)
	select {
	case <-s.source.(*retrySource).calls:
	case <-time.After(time.Second):
		t.Fatal("pending reconciliation was not retried while connected")
	}
}

// TestInFlightPollCannotClearNewResync 验证旧请求不会清除其发起后的新协调请求。
func TestInFlightPollCannotClearNewResync(t *testing.T) {
	s := newTestService(&fakeKernel{})
	s.wsResyncVersion = 2
	s.wsResyncPending.Store(true)
	s.applyPullResult(context.Background(), pullResult{resyncVersion: 1})
	if !s.wsResyncPending.Load() {
		t.Fatal("older poll cleared a newer reconciliation request")
	}
}

// TestFailedUserUpdateRetriesPreviousSnapshot 验证部分失败后旧快照不会被误判为已应用。
func TestFailedUserUpdateRetriesPreviousSnapshot(t *testing.T) {
	for _, viaREST := range []bool{false, true} {
		t.Run(map[bool]string{false: "ws", true: "rest"}[viaREST], func(t *testing.T) {
			k := &fakeKernel{running: true, updateErr: errors.New("update failed"), startErr: errors.New("start failed")}
			s := newTestService(k)
			s.cfg = &config.Config{}
			s.lastConfig = &model.NodeSpec{Protocol: "vless"}
			old := []model.UserSpec{{ID: 1, UUID: "old"}}
			s.updateUserState(old)
			s.appliedState.Config = s.lastConfig
			s.appliedState.Users = old
			s.applyUserUpdate(context.Background(), []model.UserSpec{{ID: 2, UUID: "new"}}, "")
			k.updateErr, k.startErr = nil, nil
			if viaREST {
				s.applyPullResult(context.Background(), pullResult{users: old, userHash: computeUserHash(old)})
			} else {
				s.handleWSEvent(context.Background(), controlplane.Event{Type: controlplane.EventSyncUsers, Users: old})
			}
			if k.startCalls < 2 || s.appliedState.Config == nil {
				t.Fatalf("previous snapshot was not reapplied: starts=%d applied=%+v", k.startCalls, s.appliedState)
			}
		})
	}
}

// TestStoppedKernelAcceptsEmptySnapshot 验证停止状态下空快照仍清除服务用户状态。
func TestStoppedKernelAcceptsEmptySnapshot(t *testing.T) {
	k := &fakeKernel{}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "old"}})
	s.applyUserUpdate(context.Background(), []model.UserSpec{}, computeUserHash(nil))
	if len(s.lastUsers) != 0 || s.lastUserHash != computeUserHash(nil) || k.startCalls != 0 {
		t.Fatalf("empty snapshot ignored: users=%v starts=%d", s.lastUsers, k.startCalls)
	}
}

// TestFirstUsersStartKernel 验证空用户启动后全量和增量用户均能启动内核。
func TestFirstUsersStartKernel(t *testing.T) {
	for _, delta := range []bool{false, true} {
		t.Run(map[bool]string{false: "full", true: "delta"}[delta], func(t *testing.T) {
			k := &fakeKernel{}
			s := newTestService(k)
			s.cfg = &config.Config{}
			s.lastConfig = &model.NodeSpec{Protocol: "vless"}
			s.updateUserState(nil)
			users := []model.UserSpec{{ID: 1, UUID: "first", SpeedLimit: 8}}
			if delta {
				s.applyUserDelta(context.Background(), "add", users)
			} else {
				s.applyUserUpdate(context.Background(), users, computeUserHash(users))
			}
			if !k.running || k.startCalls != 1 || len(s.lastUsers) != 1 {
				t.Fatalf("running=%v startCalls=%d users=%v", k.running, k.startCalls, s.lastUsers)
			}
		})
	}
}

// TestFailedConfigIsRetried 验证重载和备用启动失败后相同配置仍可重试。
func TestFailedConfigIsRetried(t *testing.T) {
	k := &retryKernel{fakeKernel: &fakeKernel{running: true, startErr: errors.New("start failed")}, reloadErr: errors.New("reload failed")}
	s := newTestService(k.fakeKernel)
	s.kernel = k
	s.cfg = &config.Config{}
	old := &model.NodeSpec{Protocol: "vless", ServerPort: 1000}
	next := &model.NodeSpec{Protocol: "vless", ServerPort: 2000}
	s.lastConfig = old
	s.lastConfigHash = computeConfigHash(old)
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "one"}})
	s.appliedState.Config = old
	event := controlplane.Event{Type: controlplane.EventSyncConfig, Config: next}
	s.handleWSEvent(context.Background(), event)
	k.reloadErr = nil
	k.startErr = nil
	s.handleWSEvent(context.Background(), event)
	if k.startCalls != 2 || k.stopCalls != 1 || s.appliedState.Config != next {
		t.Fatalf("starts=%d stops=%d applied=%v", k.startCalls, k.stopCalls, s.appliedState.Config)
	}
}

// cacheRetrySource 记录缓存失效并提供可观测的轮询。
type cacheRetrySource struct {
	controlplane.Source
	resets int
	calls  chan struct{}
}

// SupportsPolling 表示可以通过 REST 协调。
func (r *cacheRetrySource) SupportsPolling() bool { return true }

// ResetETags 记录缓存失效次数。
func (r *cacheRetrySource) ResetETags() { r.resets++ }

// Poll 通知测试已经发起协调请求。
func (r *cacheRetrySource) Poll(context.Context) (controlplane.Snapshot, error) {
	r.calls <- struct{}{}
	return controlplane.Snapshot{}, errors.New("temporary failure")
}

// TestFailedWSConfigSchedulesConnectedRetry 验证失败后连接中的 WS 仍触发 REST 协调。
func TestFailedWSConfigSchedulesConnectedRetry(t *testing.T) {
	k := &retryKernel{fakeKernel: &fakeKernel{running: true, startErr: errors.New("start failed")}, reloadErr: errors.New("reload failed")}
	s := newTestService(k.fakeKernel)
	s.kernel = k
	s.cfg = &config.Config{}
	s.source = &cacheRetrySource{calls: make(chan struct{}, 1)}
	s.pullResults = make(chan pullResult, 1)
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 1000}
	s.lastConfigHash = computeConfigHash(s.lastConfig)
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "one"}})
	s.handleWSEvent(context.Background(), controlplane.Event{Type: controlplane.EventSyncConfig, Config: &model.NodeSpec{Protocol: "vless", ServerPort: 2000}})
	if !s.wsResyncPending.Load() || s.source.(*cacheRetrySource).resets == 0 {
		t.Fatal("failed WS configuration did not invalidate polling cache")
	}
	s.pollTick(context.Background(), true)
	select {
	case <-s.source.(*cacheRetrySource).calls:
	case <-time.After(time.Second):
		t.Fatal("connected WS suppressed reconciliation")
	}
}

// TestUnknownStopCannotGuaranteeRelease 验证 Stop 未释放资源时不将状态标为成功。
func TestUnknownStopCannotGuaranteeRelease(t *testing.T) {
	k := &retryKernel{fakeKernel: &fakeKernel{running: true}, portHeld: true, stopKeepsPort: true}
	s := newTestService(k.fakeKernel)
	s.kernel = k
	s.cfg = &config.Config{}
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	s.kernelStateUnknown = true
	s.applyUserUpdate(context.Background(), []model.UserSpec{{ID: 1, UUID: "one"}}, "")
	if k.stopCalls != 1 || !s.kernelStateUnknown || !s.wsResyncPending.Load() || s.appliedState.Config != nil {
		t.Fatalf("stops=%d unknown=%v pending=%v applied=%v", k.stopCalls, s.kernelStateUnknown, s.wsResyncPending.Load(), s.appliedState.Config)
	}
}

// TestUnknownKernelStopsBeforeRestart 验证未知状态恢复时先释放旧实例占用的端口。
func TestUnknownKernelStopsBeforeRestart(t *testing.T) {
	k := &retryKernel{fakeKernel: &fakeKernel{running: true}, portHeld: true}
	s := newTestService(k.fakeKernel)
	s.kernel = k
	s.cfg = &config.Config{}
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	s.kernelStateUnknown = true
	s.applyUserUpdate(context.Background(), []model.UserSpec{{ID: 1, UUID: "one"}}, "")
	if k.stopCalls != 1 || s.kernelStateUnknown || s.appliedState.Config == nil {
		t.Fatalf("stops=%d unknown=%v applied=%v", k.stopCalls, s.kernelStateUnknown, s.appliedState.Config)
	}
}
