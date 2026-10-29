package controller

import (
	"context"
	"testing"
	"time"

	"branchbox/internal/api"
)

func TestRouteHubLongPoll(t *testing.T) {
	h := NewRouteHub([]api.Service{{Name: "paymentservice", Port: 50051}})
	v := h.Table().Version

	got := make(chan api.RouteTable, 1)
	go func() { got <- h.Wait(context.Background(), v) }()

	select {
	case <-got:
		t.Fatal("Wait returned before any change")
	case <-time.After(50 * time.Millisecond):
	}
	h.Set("paymentservice", "sb-1", "bb-sb-1-paymentservice:50051")
	select {
	case rt := <-got:
		if rt.Version != v+1 || len(rt.Routes) != 1 || rt.Routes[0].Env != "sb-1" {
			t.Fatalf("unexpected table %+v", rt)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not wake up on change")
	}

	h.Remove("paymentservice", "sb-1")
	if rt := h.Table(); len(rt.Routes) != 0 || rt.Version != v+2 {
		t.Fatalf("route not removed: %+v", rt)
	}
	h.Remove("paymentservice", "sb-1") // no-op must not bump the version
	if h.Table().Version != v+2 {
		t.Fatal("removing a missing route bumped the version")
	}
}

func TestRouteHubWaitTimeout(t *testing.T) {
	h := NewRouteHub(nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if rt := h.Wait(ctx, h.Table().Version); rt.Version != 1 {
		t.Fatalf("version %d", rt.Version)
	}
}
