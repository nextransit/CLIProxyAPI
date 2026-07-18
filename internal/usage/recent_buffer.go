package usage

import "sync"

// RecentBuffer is a fixed-capacity ring buffer of UsageEvent records,
// indexed by a monotonically increasing ID. It supports Since(sinceID)
// for replay-on-reconnect. Not safe for concurrent use; callers must
// serialize Push/Since via a mutex (see RequestStatistics).
type RecentBuffer struct {
	mu   sync.Mutex
	buf  [256]UsageEvent
	head uint64 // number of Push calls completed
}

func (r *RecentBuffer) Push(evt UsageEvent) {
	r.mu.Lock()
	r.buf[r.head%256] = evt
	r.head++
	r.mu.Unlock()
}

// Since returns events with id > sinceID in ascending order.
func (r *RecentBuffer) Since(sinceID uint64) []UsageEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.head == 0 {
		return nil
	}
	// Walk from oldest to newest, skipping events with id <= sinceID.
	out := make([]UsageEvent, 0, 32)
	oldest := uint64(0)
	if r.head > 256 {
		oldest = r.head - 256
	}
	for i := oldest; i < r.head; i++ {
		evt := r.buf[i%256]
		if evt.ID > sinceID {
			out = append(out, evt)
		}
	}
	return out
}

// LastID returns the most recently pushed ID, or 0 if empty.
func (r *RecentBuffer) LastID() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.head == 0 {
		return 0
	}
	return r.buf[(r.head-1)%256].ID
}
