package controller

import (
	"sort"
	"sync"

	"branchbox/internal/api"
)

// EventLog keeps the most recent routing events in memory. It is what the
// CLI's "trace" command and the end-to-end tests read, and later what the
// dashboard's live service map will be drawn from.
type EventLog struct {
	mu   sync.Mutex
	buf  []api.Event
	next int
	full bool
}

// NewEventLog returns a ring buffer holding up to size events.
func NewEventLog(size int) *EventLog { return &EventLog{buf: make([]api.Event, size)} }

// Add appends events, overwriting the oldest when full.
func (l *EventLog) Add(events []api.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, ev := range events {
		l.buf[l.next] = ev
		l.next = (l.next + 1) % len(l.buf)
		if l.next == 0 {
			l.full = true
		}
	}
}

// Filter selects events; empty fields match everything.
type Filter struct {
	TraceID string
	Env     string
	Limit   int
}

// Query returns matching events in chronological order, newest last.
func (l *EventLog) Query(f Filter) []api.Event {
	l.mu.Lock()
	n := l.next
	if l.full {
		n = len(l.buf)
	}
	out := make([]api.Event, 0, 64)
	for i := 0; i < n; i++ {
		ev := l.buf[i]
		if (f.TraceID == "" || ev.TraceID == f.TraceID) && (f.Env == "" || ev.Env == f.Env) {
			out = append(out, ev)
		}
	}
	l.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	if f.Limit > 0 && len(out) > f.Limit {
		out = out[len(out)-f.Limit:]
	}
	return out
}
