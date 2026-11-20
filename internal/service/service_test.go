package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cedar2025/xboard-node/internal/cert"
	"github.com/cedar2025/xboard-node/internal/config"
	"github.com/cedar2025/xboard-node/internal/controlplane"
	"github.com/cedar2025/xboard-node/internal/kernel"
	"github.com/cedar2025/xboard-node/internal/limiter"
	"github.com/cedar2025/xboard-node/internal/model"
	"golang.org/x/time/rate"
)

type fakeKernel struct {
	running bool

	startErr  error
	updateErr error
	addErr    error

	startCalls  int
	updateCalls int
	addCalls    int
	removeCalls int

	onUpdateUsers func([]model.UserSpec)
	onAddUsers    func([]model.UserSpec)
	onRemoveUsers func([]model.UserSpec)

	speedLimitFunc  func(string) *rate.Limiter
	deviceLimitFunc func(string) (int, bool)
}

func (f *fakeKernel) Name() string                      { return "fake" }
func (f *fakeKernel) Protocols() []string               { return []string{"vless"} }
func (f *fakeKernel) Capabilities() kernel.Capabilities { return kernel.Capabilities{} }
func (f *fakeKernel) Start(nodeConfig *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	_, _, _ = nodeConfig, users, tls
	f.startCalls++
	if f.startErr != nil {
		return f.startErr
	}
	f.running = true
	return nil
}
func (f *fakeKernel) Stop()           { f.running = false }
func (f *fakeKernel) IsRunning() bool { return f.running }
func (f *fakeKernel) Reload(nodeConfig *model.NodeSpec, users []model.UserSpec, tls kernel.TLSCert) error {
	_, _, _ = nodeConfig, users, tls
	return nil
}
func (f *fakeKernel) AddUsers(users []model.UserSpec) (int, error) {
	f.addCalls++
	if f.onAddUsers != nil {
		f.onAddUsers(users)
	}
	if f.addErr != nil {
		return 0, f.addErr
	}
	return len(users), nil
}
func (f *fakeKernel) RemoveUsers(users []model.UserSpec) (int, error) {
	f.removeCalls++
	if f.onRemoveUsers != nil {
		f.onRemoveUsers(users)
	}
	return len(users), nil
}
func (f *fakeKernel) UpdateUsers(users []model.UserSpec) (int, int, error) {
	f.updateCalls++
	if f.onUpdateUsers != nil {
		f.onUpdateUsers(users)
	}
	if f.updateErr != nil {
		return 0, 0, f.updateErr
	}
	return len(users), 0, nil
}
func (f *fakeKernel) GetUserTraffic(ctx context.Context) (map[int][2]int64, map[int]map[string]bool, int, error) {
	_ = ctx
	return nil, nil, 0, nil
}
func (f *fakeKernel) CloseConnection(ctx context.Context, connID string) error {
	_, _ = ctx, connID
	return nil
}
func (f *fakeKernel) CloseUserConnections(ctx context.Context, uuid string) error {
	_, _ = ctx, uuid
	return nil
}
func (f *fakeKernel) SetSpeedLimitFunc(fn func(uuid string) *rate.Limiter) { f.speedLimitFunc = fn }
func (f *fakeKernel) SetDeviceLimitFunc(fn func(uuid string) (int, bool))  { f.deviceLimitFunc = fn }
func (f *fakeKernel) UpdateGlobalDevices(users map[int][]string)           { _ = users }
func (f *fakeKernel) ClearGlobalDevices()                                  {}

func newTestService(k *fakeKernel) *Service {
	sharedLimiter := limiter.New()
	s := &Service{
		kernel:       k,
		limiter:      sharedLimiter,
		speedTracker: limiter.NewSpeedTracker(sharedLimiter),
		cert:         cert.NewManager(config.CertConfig{}),
	}
	k.SetSpeedLimitFunc(s.speedTracker.GetLimiter)
	k.SetDeviceLimitFunc(s.limiter.GetDeviceLimitByUUID)
	return s
}

func TestApplyUserUpdatePreparesLimiterBeforeKernelUpdate(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)

	newUsers := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	k.onUpdateUsers = func(users []model.UserSpec) {
		if len(users) != 1 || users[0].UUID != "uuid-new" {
			t.Fatalf("unexpected users passed to UpdateUsers: %#v", users)
		}
		if got := k.speedLimitFunc("uuid-new"); got == nil {
			t.Fatal("expected new user's limiter to be visible before kernel UpdateUsers")
		}
	}

	s.applyUserUpdate(context.Background(), newUsers, computeUserHash(newUsers))

	if got := k.updateCalls; got != 1 {
		t.Fatalf("UpdateUsers call count = %d, want 1", got)
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-new" {
		t.Fatalf("lastUsers = %#v, want new users", s.lastUsers)
	}
	if s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatal("expected limiter for new user after successful update")
	}
}

func TestApplyUserUpdateRestoresStateWhenKernelAndRestartFail(t *testing.T) {
	k := &fakeKernel{
		running:   true,
		updateErr: errors.New("update failed"),
		startErr:  errors.New("restart failed"),
	}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)
	oldHash := s.lastUserHash

	newUsers := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	s.applyUserUpdate(context.Background(), newUsers, computeUserHash(newUsers))

	if got := k.startCalls; got != 1 {
		t.Fatalf("Start call count = %d, want 1", got)
	}
	if len(s.lastUsers) != 1 || s.lastUsers[0].UUID != "uuid-old" {
		t.Fatalf("lastUsers = %#v, want restored old users", s.lastUsers)
	}
	if s.lastUserHash != oldHash {
		t.Fatalf("lastUserHash = %q, want %q", s.lastUserHash, oldHash)
	}
	if s.speedTracker.GetLimiter("uuid-old") == nil {
		t.Fatal("expected old limiter to be restored after rollback")
	}
	if s.speedTracker.GetLimiter("uuid-new") != nil {
		t.Fatal("expected new limiter to be removed after rollback")
	}
}

func TestApplyUserDeltaAddPreparesLimiterBeforeKernelUpdate(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newTestService(k)
	s.lastConfig = &model.NodeSpec{Protocol: "vless"}
	oldUsers := []model.UserSpec{{ID: 1, UUID: "uuid-old", SpeedLimit: 4}}
	s.updateUserState(oldUsers)

	delta := []model.UserSpec{{ID: 2, UUID: "uuid-new", SpeedLimit: 8}}
	k.onAddUsers = func(users []model.UserSpec) {
		if len(users) != 1 || users[0].UUID != "uuid-new" {
			t.Fatalf("unexpected users passed to AddUsers: %#v", users)
		}
		if got := k.speedLimitFunc("uuid-new"); got == nil {
			t.Fatal("expected delta user's limiter to be visible before kernel AddUsers")
		}
	}

	s.applyUserDelta(context.Background(), "add", delta)

	if got := k.addCalls; got != 1 {
		t.Fatalf("AddUsers call count = %d, want 1", got)
	}
	if s.speedTracker.GetLimiter("uuid-new") == nil {
		t.Fatal("expected limiter for delta-added user after successful update")
	}
}

func TestValidateNodeRuntimeRejectsUnsupportedDNSProvider(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"http"}, &model.NodeSpec{
		Protocol: "http",
		CertConfig: &config.CertConfig{
			CertMode:    "dns",
			DNSProvider: "3123123",
			Domain:      "example.com",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error")
	}
	if got := err.Error(); !strings.HasPrefix(got, `unsupported cert_config.dns_provider "3123123" (supported: `) {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestValidateNodeRuntimeAllowsSelfManagedTLSBeforeFilesExist(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"anytls", "hysteria"}, &model.NodeSpec{
		Protocol: "anytls",
		CertConfig: &config.CertConfig{
			CertMode: "self",
			Domain:   "example.com",
		},
	}, kernel.TLSCert{})
	if err != nil {
		t.Fatalf("expected self-managed TLS config to pass validation, got %v", err)
	}
}

func TestValidateNodeRuntimeAllowsSingboxRealityWithRequiredFields(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
		TLSSettings: map[string]any{
			"private_key": "test-key",
			"server_name": "example.com",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err != nil {
		t.Fatalf("expected sing-box reality validation to pass, got %v", err)
	}
}

func TestValidateNodeRuntimeRejectsRealityWithoutTLSSettings(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "reality tls requires tls_settings" {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestValidateNodeRuntimeRejectsRealityWithoutPrivateKey(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
		TLSSettings: map[string]any{
			"server_name": "example.com",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "reality tls requires tls_settings.private_key" {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestValidateNodeRuntimeRejectsRealityWithoutServerNameOrDest(t *testing.T) {
	cfg := &config.Config{Kernel: config.KernelConfig{Type: "singbox"}}
	err := validateNodeRuntime(cfg, []string{"vless"}, &model.NodeSpec{
		Protocol: "vless",
		TLS:      2,
		TLSSettings: map[string]any{
			"private_key": "test-key",
		},
	}, kernel.TLSCert{CertPEM: []byte("CERT"), KeyPEM: []byte("KEY")})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "reality tls requires tls_settings.server_name or tls_settings.dest" {
		t.Fatalf("unexpected error: %v", got)
	}
}

func TestAPIBackoffExponentialProgression(t *testing.T) {
	var b apiBackoff
	for _, wantSkips := range []int{1, 2, 4, 8, 8} {
		b.onFailure()
		skips := 0
		for b.shouldSkip() {
			skips++
		}
		if skips != wantSkips {
			t.Fatalf("backoff skips = %d, want %d", skips, wantSkips)
		}
	}
	b.onSuccess()
	if b.shouldSkip() {
		t.Fatal("expected no skip after onSuccess")
	}
	b.onFailure()
	skips := 0
	for b.shouldSkip() {
		skips++
	}
	if skips != 1 {
		t.Fatalf("expected reset to 1 skip after success, got %d", skips)
	}
}

type discoveryTestSource struct {
	*controlplane.LocalControlPlane
	discovered controlplane.PushClient
}

func (d *discoveryTestSource) SupportsDiscovery() bool { return true }
func (d *discoveryTestSource) Discover(context.Context, func() map[string]interface{}, chan<- controlplane.Event, chan<- controlplane.StatusChange) (controlplane.PushClient, error) {
	return d.discovered, nil
}

type discoveryTestPushClient struct {
	started chan struct{}
}

func (p *discoveryTestPushClient) Run(ctx context.Context) {
	close(p.started)
	<-ctx.Done()
}
func (p *discoveryTestPushClient) IsConnected() bool                 { return false }
func (p *discoveryTestPushClient) SendDeviceReport(map[int][]string) {}

func TestWSDiscoveryReplacesStaleClientAfterProlongedDisconnect(t *testing.T) {
	oldCancelled := false
	newClient := &discoveryTestPushClient{started: make(chan struct{})}
	src := &discoveryTestSource{
		LocalControlPlane: controlplane.NewLocalControlPlane(&config.Config{}),
		discovered:        newClient,
	}

	s := newService(&config.Config{}, src)
	s.wsClient = &discoveryTestPushClient{started: make(chan struct{})}
	s.wsCancel = func() { oldCancelled = true }
	s.wsDisconnectAt = time.Now().Add(-15 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	s.wsDiscovery(ctx)

	if !oldCancelled {
		t.Fatal("expected stale WS client to be cancelled")
	}
	if s.wsClient != newClient {
		t.Fatal("expected stale WS client to be replaced by newly discovered client")
	}
	select {
	case <-newClient.started:
	case <-time.After(time.Second):
		t.Fatal("expected newly discovered WS client to be started")
	}
}

func TestRemovingLastUserStopsRunningKernel(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newService(&config.Config{}, controlplane.NewLocalControlPlane(&config.Config{}))
	s.kernel = k
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 1080}
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "u1"}})

	// Case 1: full sync with empty users
	s.applyUserUpdate(context.Background(), []model.UserSpec{}, computeUserHash([]model.UserSpec{}))
	if k.IsRunning() {
		t.Fatal("expected kernel to stop when applyUserUpdate receives empty users")
	}

	// Case 2: delta remove of last user
	k.running = true
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "u1"}})
	s.applyUserDelta(context.Background(), "remove", []model.UserSpec{{ID: 1, UUID: "u1"}})
	if k.IsRunning() {
		t.Fatal("expected kernel to stop when applyUserDelta removes last user")
	}
}

func TestStalePullResultDoesNotOverwriteNewerWSState(t *testing.T) {
	k := &fakeKernel{running: true}
	s := newService(&config.Config{}, controlplane.NewLocalControlPlane(&config.Config{}))
	s.kernel = k
	s.lastConfig = &model.NodeSpec{Protocol: "vless", ServerPort: 443}
	s.lastConfigHash = computeConfigHash(s.lastConfig)
	s.updateUserState([]model.UserSpec{{ID: 1, UUID: "u1"}})

	// Snapshot version before WS mutates users.
	oldVersion := s.stateVersion
	s.handleWSEvent(context.Background(), controlplane.Event{
		Type:        controlplane.EventSyncUserDelta,
		DeltaAction: "add",
		DeltaUsers:  []model.UserSpec{{ID: 2, UUID: "u2"}},
	})
	if len(s.lastUsers) != 2 {
		t.Fatalf("expected 2 users after WS delta, got %d", len(s.lastUsers))
	}

	// Stale REST result arrives with oldVersion and [u1].
	staleUsers := []model.UserSpec{{ID: 1, UUID: "u1"}}
	s.applyPullResult(context.Background(), pullResult{
		users:        staleUsers,
		userHash:     computeUserHash(staleUsers),
		stateVersion: oldVersion,
	})
	if len(s.lastUsers) != 2 {
		t.Fatalf("stale REST pull overwrote newer WS users: got %+v", s.lastUsers)
	}
}

func TestStartWithUsersBuffersWhenConfigNil(t *testing.T) {
	k := &fakeKernel{running: false}
	s := newService(&config.Config{}, controlplane.NewLocalControlPlane(&config.Config{}))
	s.kernel = k
	s.lastConfig = nil

	// WS users arrive before config.
	s.handleWSEvent(context.Background(), controlplane.Event{
		Type:  controlplane.EventSyncUsers,
		Users: []model.UserSpec{{ID: 1, UUID: "u1"}},
	})
	if len(s.lastUsers) != 1 {
		t.Fatalf("expected users buffered when lastConfig is nil, got %d", len(s.lastUsers))
	}

	// Config arrives next and must start kernel with buffered users.
	s.handleWSEvent(context.Background(), controlplane.Event{
		Type:   controlplane.EventSyncConfig,
		Config: &model.NodeSpec{Protocol: "vless", ServerPort: 443},
	})
	if !k.IsRunning() {
		t.Fatal("expected kernel to start once config arrived after buffered users")
	}
}
