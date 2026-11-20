package tracker

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/cedar2025/xboard-node/internal/nlog"
)

// snapshot is an immutable point-in-time view of tracker state.
// It is swapped atomically so readers never block writers.
type snapshot struct {
	traffic   map[int][2]int64        // userID → [upload, download] delta
	aliveIPs  map[int]map[string]bool // userID → set of source IPs
	online    map[int]int             // userID → distinct IP count
	connCount int
	inSpeed   int64
	outSpeed  int64
}

// Tracker computes per-user traffic deltas from cumulative counters
// provided by the kernel, and accumulates totals for panel reporting.
//
// Architecture: the kernel maintains per-user atomic counters and IP sets.
// Each tick, the service calls Process() with cumulative per-user traffic.
// Tracker computes deltas against the previous cycle's values — O(users),
// not O(connections).
//
// Thread safety: Process() acquires mu to update internal state, then
// atomically publishes a new snapshot. All read methods (Flush*,
// CurrentOnline, LogStats, *Speed) read the snapshot lock-free.
// This eliminates contention between the 10s Process tick and the 60s
// flush/push tick.
type Tracker struct {
	mu sync.Mutex

	// lastSeen stores the cumulative traffic from the previous Process() call.
	// Protected by mu — only written by Process.
	lastSeen map[int][2]int64 // userID → [upload, download] cumulative

	// pending accumulates deltas between flushes.
	// Protected by mu — written by Process, drained by FlushTraffic.
	pendingTraffic map[int][2]int64

	// live holds the current snapshot, swapped atomically.
	// Readers load this pointer without any lock.
	live atomic.Pointer[snapshot]

	// restoredAliveIPs holds alive IPs restored after a failed panel report,
	// merged into the next FlushAliveIPs() call. Protected by mu.
	restoredAliveIPs map[int][]string

	// lastAliveIPsHash detects changes for periodic panel reports.
	lastAliveIPsHash string

	// lastDeviceReportHash detects changes independently for WS device reports.
	lastDeviceReportHash string

	// trackIntervalSec is the sampling interval in seconds for speed calculation.
	trackIntervalSec atomic.Int64
}

func New() *Tracker {
	t := &Tracker{
		lastSeen:         make(map[int][2]int64),
		pendingTraffic:   make(map[int][2]int64),
		restoredAliveIPs: make(map[int][]string),
	}
	t.trackIntervalSec.Store(10)
	// Publish initial empty snapshot.
	t.live.Store(&snapshot{
		traffic:  make(map[int][2]int64),
		aliveIPs: make(map[int]map[string]bool),
		online:   make(map[int]int),
	})
	return t
}

// SetTrackInterval configures the tracking interval in seconds used to compute
// per-second inbound/outbound speeds. Values <= 0 default to 10s.
func (t *Tracker) SetTrackInterval(seconds int) {
	if seconds <= 0 {
		seconds = 10
	}
	t.trackIntervalSec.Store(int64(seconds))
}

// Process computes per-user traffic deltas from cumulative kernel counters.
// Also stores alive IPs and connection count. O(users).
//
// This is the only writer to lastSeen and pendingTraffic.
// After computing deltas, it publishes a new snapshot for lock-free reads.
func (t *Tracker) Process(
	cumTraffic map[int][2]int64,
	kernelAliveIPs map[int]map[string]bool,
	connCount int,
) {
	t.mu.Lock()
	defer t.mu.Unlock()

	var cycleIn, cycleOut int64

	for uid, cum := range cumTraffic {
		prev := t.lastSeen[uid]
		deltaUp := cum[0] - prev[0]
		deltaDown := cum[1] - prev[1]

		// Guard against counter reset (kernel restart).
		if deltaUp < 0 {
			deltaUp = cum[0]
		}
		if deltaDown < 0 {
			deltaDown = cum[1]
		}

		t.lastSeen[uid] = cum

		if deltaUp > 0 || deltaDown > 0 {
			cur := t.pendingTraffic[uid]
			cur[0] += deltaUp
			cur[1] += deltaDown
			t.pendingTraffic[uid] = cur

			cycleOut += deltaUp
			cycleIn += deltaDown
		}
	}

	// Compute online from alive IPs.
	online := make(map[int]int, len(kernelAliveIPs))
	for uid, ips := range kernelAliveIPs {
		online[uid] = len(ips)
	}

	// Publish new snapshot (readers will see this atomically).
	t.live.Store(&snapshot{
		traffic:   copyTrafficMap(t.pendingTraffic),
		aliveIPs:  kernelAliveIPs, // kernel provides fresh copy each tick
		online:    online,
		connCount: connCount,
		inSpeed:   cycleIn,
		outSpeed:  cycleOut,
	})
}

// FlushTraffic returns accumulated per-user traffic and resets the pending buffer.
// Lock-free: reads from live snapshot, then acquires mu only to drain.
func (t *Tracker) FlushTraffic() map[int][2]int64 {
	t.mu.Lock()
	data := t.pendingTraffic
	t.pendingTraffic = make(map[int][2]int64, len(data))
	t.mu.Unlock()
	return data
}

// RestoreTraffic adds traffic back (used when push to panel fails).
func (t *Tracker) RestoreTraffic(data map[int][2]int64) {
	t.mu.Lock()
	for uid, d := range data {
		cur := t.pendingTraffic[uid]
		cur[0] += d[0]
		cur[1] += d[1]
		t.pendingTraffic[uid] = cur
	}
	t.mu.Unlock()
}

// HasTraffic returns true if there is accumulated traffic to report.
func (t *Tracker) HasTraffic() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.pendingTraffic) > 0
}

// FlushAliveIPs returns a fresh copy of per-user alive IPs for panel reporting.
// Returns nil if unchanged and no previously failed alive IPs are pending restore.
func (t *Tracker) FlushAliveIPs() map[int][]string {
	s := t.live.Load()

	t.mu.Lock()
	defer t.mu.Unlock()

	hasRestored := len(t.restoredAliveIPs) > 0
	currentHash := calcAliveIPsHash(s.aliveIPs)

	// If no changes and nothing restored from a failed report, return nil.
	if !hasRestored && currentHash == t.lastAliveIPsHash {
		return nil
	}

	t.lastAliveIPsHash = currentHash

	result := make(map[int][]string, len(s.aliveIPs)+len(t.restoredAliveIPs))
	for uid, ips := range s.aliveIPs {
		list := make([]string, 0, len(ips))
		for ip := range ips {
			list = append(list, ip)
		}
		result[uid] = list
	}
	if hasRestored {
		for uid, ipList := range t.restoredAliveIPs {
			existing := result[uid]
			existSet := make(map[string]struct{}, len(existing)+len(ipList))
			for _, ip := range existing {
				existSet[ip] = struct{}{}
			}
			for _, ip := range ipList {
				if _, ok := existSet[ip]; !ok {
					existing = append(existing, ip)
					existSet[ip] = struct{}{}
				}
			}
			result[uid] = existing
		}
		t.restoredAliveIPs = make(map[int][]string)
	}

	return result
}

// ResetCumulativeBaseline clears cached cumulative kernel counters so a fresh
// kernel instance starting from zero does not undercount post-restart traffic.
func (t *Tracker) ResetCumulativeBaseline() {
	t.mu.Lock()
	t.lastSeen = make(map[int][2]int64)
	t.mu.Unlock()
}

// ResetDeviceReportHash clears the WS device-report deduplication hash so the
// next FlushDeviceReport re-sends active devices after a WS reconnect.
func (t *Tracker) ResetDeviceReportHash() {
	t.mu.Lock()
	t.lastDeviceReportHash = ""
	t.mu.Unlock()
}

// FlushDeviceReport returns a fresh copy of per-user alive IPs for WS device
// reporting, using its own deduplication hash independent of FlushAliveIPs.
func (t *Tracker) FlushDeviceReport() map[int][]string {
	s := t.live.Load()

	t.mu.Lock()
	defer t.mu.Unlock()

	currentHash := calcAliveIPsHash(s.aliveIPs)
	if currentHash == t.lastDeviceReportHash {
		return nil
	}
	t.lastDeviceReportHash = currentHash

	result := make(map[int][]string, len(s.aliveIPs))
	for uid, ips := range s.aliveIPs {
		list := make([]string, 0, len(ips))
		for ip := range ips {
			list = append(list, ip)
		}
		result[uid] = list
	}
	return result
}

// calcAliveIPsHash computes a deterministic hash for change detection.
func calcAliveIPsHash(aliveIPs map[int]map[string]bool) string {
	if len(aliveIPs) == 0 {
		return ""
	}

	h := sha256.New()

	// Sort user IDs for consistent hashing
	userIDs := make([]int, 0, len(aliveIPs))
	for uid := range aliveIPs {
		userIDs = append(userIDs, uid)
	}
	sort.Ints(userIDs)

	for _, uid := range userIDs {
		ips := aliveIPs[uid]
		// Sort IPs for consistent hashing
		ipList := make([]string, 0, len(ips))
		for ip := range ips {
			ipList = append(ipList, ip)
		}
		sort.Strings(ipList)

		// Write user ID and IPs to hash
		h.Write([]byte{byte(uid >> 24), byte(uid >> 16), byte(uid >> 8), byte(uid)})
		for _, ip := range ipList {
			h.Write([]byte(ip))
		}
	}

	return hex.EncodeToString(h.Sum(nil))
}

// CurrentOnline returns a snapshot copy of user_id → device count (distinct IPs).
// Lock-free: reads from live snapshot.
func (t *Tracker) CurrentOnline() map[int]int {
	s := t.live.Load()
	cp := make(map[int]int, len(s.online))
	for k, v := range s.online {
		cp[k] = v
	}
	return cp
}

// RestoreAliveIPs merges alive IPs back in (used when push to panel fails)
// and invalidates the deduplication hash so the next FlushAliveIPs retries them.
func (t *Tracker) RestoreAliveIPs(data map[int][]string) {
	if len(data) == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastAliveIPsHash = ""
	for uid, ipList := range data {
		ips := t.restoredAliveIPs[uid]
		if ips == nil {
			ips = make([]string, 0, len(ipList))
		}
		existMap := make(map[string]struct{}, len(ips)+len(ipList))
		for _, existing := range ips {
			existMap[existing] = struct{}{}
		}
		for _, ip := range ipList {
			if _, exists := existMap[ip]; !exists {
				ips = append(ips, ip)
				existMap[ip] = struct{}{}
			}
		}
		t.restoredAliveIPs[uid] = ips
	}
}

// LogStats logs current tracking statistics.
// Lock-free: reads from live snapshot.
func (t *Tracker) LogStats() {
	s := t.live.Load()
	nlog.TrackerStats(s.connCount, len(s.online))
}

// ActiveConnections returns the last observed active connection count.
// Lock-free: reads from live snapshot.
func (t *Tracker) ActiveConnections() int {
	return t.live.Load().connCount
}

// TotalConnections is deprecated — no longer tracked per-connection.
// Returns 0 for backward compatibility.
func (t *Tracker) TotalConnections() int64 {
	return 0
}

// InboundSpeed returns the last observed inbound (download) speed in bytes/second.
// Lock-free: reads from live snapshot.
func (t *Tracker) InboundSpeed() int64 {
	interval := t.trackIntervalSec.Load()
	if interval <= 0 {
		interval = 10
	}
	return t.live.Load().inSpeed / interval
}

// OutboundSpeed returns the last observed outbound (upload) speed in bytes/second.
// Lock-free: reads from live snapshot.
func (t *Tracker) OutboundSpeed() int64 {
	interval := t.trackIntervalSec.Load()
	if interval <= 0 {
		interval = 10
	}
	return t.live.Load().outSpeed / interval
}

// copyTrafficMap creates a shallow copy of the traffic map.
func copyTrafficMap(src map[int][2]int64) map[int][2]int64 {
	dst := make(map[int][2]int64, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
