package routing

import (
	"testing"

	"branchbox/internal/api"
)

func TestLookupAndServiceFor(t *testing.T) {
	tbl := NewTable()
	tbl.store(api.RouteTable{
		Version:  3,
		Services: []api.Service{{Name: "paymentservice", Port: 50051}, {Name: "shippingservice", Port: 50051}},
		Routes:   []api.Route{{Service: "paymentservice", Env: "sb-1", Address: "bb-sb-1-paymentservice:50051"}},
	})

	if addr, ok := tbl.Lookup("paymentservice", "sb-1"); !ok || addr != "bb-sb-1-paymentservice:50051" {
		t.Fatalf("lookup = %q %v", addr, ok)
	}
	for _, miss := range [][2]string{{"paymentservice", ""}, {"paymentservice", "sb-2"}, {"shippingservice", "sb-1"}} {
		if _, ok := tbl.Lookup(miss[0], miss[1]); ok {
			t.Errorf("%v must fall back to the baseline", miss)
		}
	}

	// Both services listen on 50051, so the authority, not the port,
	// must identify the destination.
	if got := tbl.ServiceFor("shippingservice:50051", "10.0.0.9:50051"); got != "shippingservice" {
		t.Errorf("ServiceFor = %q", got)
	}
	if got := tbl.ServiceFor("PaymentService.default.svc:50051", ""); got != "paymentservice" {
		t.Errorf("FQDN ServiceFor = %q", got)
	}
	tbl.ipMap = map[string]string{"10.0.0.7:50051": "paymentservice"}
	if got := tbl.ServiceFor("10.0.0.7:50051", "10.0.0.7:50051"); got != "paymentservice" {
		t.Errorf("IP fallback ServiceFor = %q", got)
	}
	if got := tbl.ServiceFor("redis-cart:6379", "10.0.0.3:6379"); got != "" {
		t.Errorf("non-project destination identified as %q", got)
	}
}
