package limiter

import (
	"testing"
	"time"

	"github.com/cedar2025/xboard-node/internal/model"
	"golang.org/x/time/rate"
)

func TestSpeedTracker_UpdateBuckets(t *testing.T) {
	l := New()
	st := NewSpeedTracker(l)

	users := []model.UserSpec{
		{ID: 1, UUID: "u1", SpeedLimit: 3, DeviceLimit: 0},  // 3 Mbps
		{ID: 2, UUID: "u2", SpeedLimit: 0, DeviceLimit: 0},  // unlimited
		{ID: 3, UUID: "u3", SpeedLimit: 10, DeviceLimit: 2}, // 10 Mbps
	}
	l.UpdateUsers(users)
	st.UpdateBuckets()

	// User 1: 3 Mbps = 375000 bytes/sec
	b1 := st.GetLimiter("u1")
	if b1 == nil {
		t.Fatal("expected bucket for user 1")
	}
	expectedRate := float64(3 * 1000000 / 8)
	if float64(b1.Limit()) != expectedRate {
		t.Errorf("user 1 rate: got %v, want %v", float64(b1.Limit()), expectedRate)
	}
	// Burst should be max(bytesPerSec, 64KB) = max(375000, 65536) = 375000
	expectedBurst := 3 * 1000000 / 8
	if b1.Burst() != expectedBurst {
		t.Errorf("user 1 burst: got %v, want %v", b1.Burst(), expectedBurst)
	}

	// User 2: no speed limit, no bucket
	b2 := st.GetLimiter("u2")
	if b2 != nil {
		t.Fatal("expected no bucket for user 2")
	}

	// User 3: 10 Mbps = 1250000 bytes/sec
	b3 := st.GetLimiter("u3")
	if b3 == nil {
		t.Fatal("expected bucket for user 3")
	}
	expectedRate3 := float64(10 * 1000000 / 8)
	if float64(b3.Limit()) != expectedRate3 {
		t.Errorf("user 3 rate: got %v, want %v", float64(b3.Limit()), expectedRate3)
	}
	// Burst should be max(1250000, 64KB) = 1250000
	expectedBurst3 := 10 * 1000000 / 8
	if b3.Burst() != expectedBurst3 {
		t.Errorf("user 3 burst: got %v, want %v", b3.Burst(), expectedBurst3)
	}
}

func TestSpeedTracker_UpdateBuckets_PreservesExisting(t *testing.T) {
	l := New()
	st := NewSpeedTracker(l)

	users := []model.UserSpec{
		{ID: 1, UUID: "u1", SpeedLimit: 3, DeviceLimit: 0},
	}
	l.UpdateUsers(users)
	st.UpdateBuckets()

	b1 := st.GetLimiter("u1")
	if b1 == nil {
		t.Fatal("expected bucket for user 1")
	}

	// Update same user with different speed — should reuse same *rate.Limiter pointer
	users2 := []model.UserSpec{
		{ID: 1, UUID: "u1", SpeedLimit: 5, DeviceLimit: 0},
	}
	l.UpdateUsers(users2)
	st.UpdateBuckets()

	b1After := st.GetLimiter("u1")
	if b1After == nil {
		t.Fatal("expected bucket for user 1 after update")
	}
	if b1 != b1After {
		t.Error("expected same rate.Limiter instance to be reused")
	}

	// Rate should be updated
	expectedRate := float64(5 * 1000000 / 8)
	if float64(b1After.Limit()) != expectedRate {
		t.Errorf("user 1 updated rate: got %v, want %v", float64(b1After.Limit()), expectedRate)
	}
}

func TestSpeedTracker_UpdateBuckets_RemovesUsers(t *testing.T) {
	l := New()
	st := NewSpeedTracker(l)

	users := []model.UserSpec{
		{ID: 1, UUID: "u1", SpeedLimit: 3, DeviceLimit: 0},
		{ID: 2, UUID: "u2", SpeedLimit: 5, DeviceLimit: 0},
	}
	l.UpdateUsers(users)
	st.UpdateBuckets()

	if st.GetLimiter("u1") == nil || st.GetLimiter("u2") == nil {
		t.Fatal("expected buckets for both users")
	}

	// Remove user 2
	users2 := []model.UserSpec{
		{ID: 1, UUID: "u1", SpeedLimit: 3, DeviceLimit: 0},
	}
	l.UpdateUsers(users2)
	st.UpdateBuckets()

	if st.GetLimiter("u1") == nil {
		t.Fatal("expected bucket for user 1")
	}
	if st.GetLimiter("u2") != nil {
		t.Error("expected no bucket for removed user 2")
	}
}

// Regression: log callback must not run while UpdateBuckets holds t.mu.Lock,
// otherwise callbacks that call LimitedUserCount() self-deadlock on RWMutex.
func TestSpeedTracker_UpdateBuckets_LogCallbackMayCallLimitedUserCount(t *testing.T) {
	l := New()
	st := NewSpeedTracker(l)
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "u1", SpeedLimit: 1}})
	st.SetLogCallback(func(msg string) {
		_ = st.LimitedUserCount()
	})

	done := make(chan struct{})
	go func() {
		st.UpdateBuckets()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: UpdateBuckets did not finish (log callback vs RWMutex)")
	}
}

func TestSpeedTracker_HasLimitsAndUnthrottlesExistingConnections(t *testing.T) {
	l := New()
	st := NewSpeedTracker(l)

	// User has speed limit configured, but has not connected yet (no bucket created).
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "u1", SpeedLimit: 10}})
	st.UpdateBuckets()
	if !st.HasLimits() {
		t.Fatal("HasLimits() = false, want true when limited users exist before any connection")
	}

	// Active connection acquires limiter handle.
	lim := st.GetLimiter("u1")
	if lim == nil {
		t.Fatal("expected non-nil limiter for u1")
	}

	// Admin lifts speed limit to 0 (unlimited): existing limiter must become rate.Inf
	// so active connections are immediately unthrottled, and HasLimits() becomes false.
	l.UpdateUsers([]model.UserSpec{{ID: 1, UUID: "u1", SpeedLimit: 0}})
	st.UpdateBuckets()
	if st.HasLimits() {
		t.Fatal("HasLimits() = true, want false after all speed limits removed")
	}
	if lim.Limit() != rate.Inf {
		t.Fatalf("existing limiter Limit() = %v, want rate.Inf after clearing speed limit", lim.Limit())
	}
	if st.GetLimiter("u1") != nil {
		t.Fatal("expected nil limiter after speed limit cleared")
	}
}

func TestSpeedLimitLargeMbpsNoOverflow(t *testing.T) {
	limit, burst := speedLimitToBytesAndBurst(100_000) // 100,000 Mbps = 100 Gbps
	if limit <= 0 {
		t.Fatalf("expected positive limit, got %v", limit)
	}
	if burst <= 0 {
		t.Fatalf("expected positive burst, got %v", burst)
	}
	expectedBps := int64(100_000) * 1_000_000 / 8
	if int64(limit) != expectedBps {
		t.Fatalf("limit = %v, want %v", limit, expectedBps)
	}
	expectedBurst := expectedBps
	if int64(burst) != expectedBurst {
		t.Fatalf("burst = %v, want %v", burst, expectedBurst)
	}
}
