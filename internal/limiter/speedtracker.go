package limiter

import (
	"math"
	"sync"
	"sync/atomic"

	"github.com/cedar2025/xboard-node/internal/model"
	"golang.org/x/time/rate"
)

// SpeedTrackerLogCallback is called when bucket updates occur.
type SpeedTrackerLogCallback func(msg string)

// SpeedTracker manages per-user token-bucket rate limiters.
// It does NOT wrap connections itself — instead, ConnTracker consults it
// via GetLimiter to embed rate limiting in the same tracked connection
// wrapper that does byte counting.
type SpeedTracker struct {
	limiter *Limiter
	mu      sync.RWMutex
	buckets map[int]*rate.Limiter // userID → shared rate limiter
	uuidMap map[string]int        // UUID → userID

	// Fast-path: when no users have a speed limit, GetLimiter returns nil
	// immediately without any map lookup.
	hasLimits atomic.Bool

	// Optional callback for logging
	logFunc SpeedTrackerLogCallback
}

// NewSpeedTracker creates a bucket manager for per-user bandwidth throttling.
func NewSpeedTracker(l *Limiter) *SpeedTracker {
	return &SpeedTracker{
		limiter: l,
		buckets: make(map[int]*rate.Limiter),
		uuidMap: make(map[string]int),
	}
}

// SetLogCallback sets the logging callback.
func (t *SpeedTracker) SetLogCallback(f SpeedTrackerLogCallback) {
	t.logFunc = f
}

// UpdateBuckets updates the UUID→userID mapping and syncs existing limiters.
func (t *SpeedTracker) UpdateBuckets() {
	currentUsers := make([]model.UserSpec, 0, 32)
	t.limiter.mu.RLock()
	for _, u := range t.limiter.users {
		currentUsers = append(currentUsers, u)
	}
	t.limiter.mu.RUnlock()

	func() {
		t.mu.Lock()
		defer t.mu.Unlock()

		newUUIDMap := make(map[string]int, len(currentUsers))
		activeIDs := make(map[int]struct{}, len(currentUsers))
		anyLimit := false

		for _, user := range currentUsers {
			activeIDs[user.ID] = struct{}{}
			if user.UUID != "" {
				newUUIDMap[user.UUID] = user.ID
			}
			if user.SpeedLimit > 0 {
				anyLimit = true
			}

			// Update existing limiter if speed changed
			if lim, ok := t.buckets[user.ID]; ok {
				if user.SpeedLimit > 0 {
					limit, burst := speedLimitToBytesAndBurst(user.SpeedLimit)
					lim.SetLimit(limit)
					lim.SetBurst(burst)
				} else {
					// Lift throttling on active connections still holding this limiter.
					lim.SetLimit(rate.Inf)
					delete(t.buckets, user.ID)
				}
			}
		}

		// Clean up buckets for removed users
		for id, lim := range t.buckets {
			if _, ok := activeIDs[id]; !ok {
				lim.SetLimit(rate.Inf)
				delete(t.buckets, id)
			}
		}

		t.uuidMap = newUUIDMap
		t.hasLimits.Store(anyLimit)
	}()

	if t.logFunc != nil {
		t.logFunc("buckets updated")
	}
}

// GetLimiter returns the rate limiter for the given user UUID, or nil if
// no limit applies. Creates limiter on-demand if not exists.
// Thread-safe.
func (t *SpeedTracker) GetLimiter(user string) *rate.Limiter {
	t.mu.RLock()
	uid, exists := t.uuidMap[user]
	if !exists {
		t.mu.RUnlock()
		return nil
	}
	if lim, ok := t.buckets[uid]; ok {
		t.mu.RUnlock()
		return lim
	}
	t.mu.RUnlock()

	// Get user info from limiter
	t.limiter.mu.RLock()
	u, userExists := t.limiter.users[uid]
	t.limiter.mu.RUnlock()

	if !userExists || u.SpeedLimit <= 0 {
		return nil
	}

	// Create limiter on-demand
	limit, burst := speedLimitToBytesAndBurst(u.SpeedLimit)
	lim := rate.NewLimiter(limit, burst)

	t.mu.Lock()
	// Double-check after acquiring write lock: user may have been removed or
	// had their limit cleared concurrently by UpdateBuckets.
	if currentUID, ok := t.uuidMap[user]; !ok || currentUID != uid {
		t.mu.Unlock()
		return nil
	}
	if existing, ok := t.buckets[uid]; ok {
		t.mu.Unlock()
		return existing
	}
	t.buckets[uid] = lim
	t.hasLimits.Store(true)
	t.mu.Unlock()

	return lim
}

// HasLimits returns true if any user currently has a speed limit configured.
func (t *SpeedTracker) HasLimits() bool {
	return t.hasLimits.Load()
}

// LimitedUserCount returns the number of users with active speed limits.
func (t *SpeedTracker) LimitedUserCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.buckets)
}

func speedLimitToBytesAndBurst(speedLimitMbps int) (rate.Limit, int) {
	if speedLimitMbps <= 0 {
		return rate.Inf, 0
	}
	// Compute in 64-bit to prevent 32-bit signed integer overflow (e.g. 2500 Mbps * 1_000_000).
	bytesPerSec := int64(speedLimitMbps) * 1_000_000 / 8
	burst := bytesPerSec
	if burst < 64*1024 {
		burst = 64 * 1024
	}
	if cap4s := bytesPerSec * 4; cap4s > 64*1024 && burst > cap4s {
		burst = cap4s
	}
	maxInt := int64(math.MaxInt)
	if burst > maxInt {
		burst = maxInt
	}
	return rate.Limit(bytesPerSec), int(burst)
}
