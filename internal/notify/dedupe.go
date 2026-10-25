package notify

import (
	"sync"
	"time"
)

// defaultDedupeWindow collapses identical notifications that arrive twice, for
// example an OpenCode ask with no tool call id, or the same event forwarded by
// two children. Zero or less disables the window.
const defaultDedupeWindow = 15 * time.Second

// eventDeduper remembers every key that identifies one request. The first
// reservation wins; a rejected reservation does not claim its other keys.
// Reservation is synchronous so two handlers that both await session info
// cannot both notify.
type eventDeduper struct {
	ttl  time.Duration
	now  func() time.Time
	mu   sync.Mutex
	seen map[string]time.Time
}

func newEventDeduper(ttl time.Duration) *eventDeduper {
	return &eventDeduper{ttl: ttl, now: time.Now, seen: map[string]time.Time{}}
}

// reserve claims keys. It returns false when any key is already live.
func (d *eventDeduper) reserve(keys []string) bool {
	if d == nil || d.ttl <= 0 || len(keys) == 0 {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	d.forgetLocked(now)
	for _, key := range keys {
		if _, ok := d.seen[key]; ok {
			return false
		}
	}
	for _, key := range keys {
		d.seen[key] = now
	}
	return true
}

// forget drops live reservations. The caller must already hold d.mu.
func (d *eventDeduper) forgetLocked(now time.Time) {
	if d == nil || d.ttl <= 0 {
		return
	}
	for key, seen := range d.seen {
		if now.Sub(seen) >= d.ttl {
			delete(d.seen, key)
		}
	}
}

func questionKeys(event questionEvent) []string {
	keys := []string{"question:" + event.ID}
	if event.CallID != "" {
		keys = append(keys, "question:tool:"+event.SessionID+":"+event.CallID)
	}
	return keys
}

func permissionKeys(event permissionEvent) []string {
	keys := []string{"permission:" + event.ID}
	if event.CallID != "" {
		keys = append(keys, "permission:tool:"+event.SessionID+":"+event.CallID)
	}
	return keys
}

// dedupeKey identifies a near-duplicate across local SSE and parent ingest.
func dedupeKey(notification Notification) string {
	source := notification.Source
	if source == "" {
		source = SourceOpenCode
	}
	base := string(notification.Type) + ":" + string(source) + ":" + notification.SessionID + ":" + notification.Hostname
	switch notification.Type {
	case TypePermission:
		return base + ":" + notification.PermissionType + ":" + notification.PermissionTitle
	case TypeQuestion:
		return base + ":" + notification.Question
	default:
		return base
	}
}
