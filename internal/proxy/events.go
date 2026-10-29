package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"branchbox/internal/api"
)

// EventSink ships routing events to the controller in small batches. It
// never blocks the request path: when the buffer is full, events are
// dropped rather than slowing traffic down.
type EventSink struct {
	ch         chan api.Event
	controller string
	token      string
	client     *http.Client
}

// NewEventSink returns a sink posting to {controller}/v1/events,
// authenticating with token when it is non-empty.
func NewEventSink(controller, token string) *EventSink {
	return &EventSink{
		ch:         make(chan api.Event, 4096),
		controller: strings.TrimRight(controller, "/"),
		token:      token,
		client:     &http.Client{Timeout: 5 * time.Second},
	}
}

// Record queues ev for delivery.
func (s *EventSink) Record(ev api.Event) {
	select {
	case s.ch <- ev:
	default:
	}
}

// Run delivers queued events until ctx is cancelled.
func (s *EventSink) Run(ctx context.Context) {
	const maxBatch = 200
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	batch := make([]api.Event, 0, maxBatch)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		body, _ := json.Marshal(batch)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.controller+"/v1/events", bytes.NewReader(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			if s.token != "" {
				req.Header.Set("Authorization", "Bearer "+s.token)
			}
			if resp, err := s.client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-s.ch:
			batch = append(batch, ev)
			if len(batch) >= maxBatch {
				flush()
			}
		case <-tick.C:
			flush()
		}
	}
}
