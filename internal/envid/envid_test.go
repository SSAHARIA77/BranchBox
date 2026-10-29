package envid

import (
	"net/http"
	"testing"
)

func TestFromHeaders(t *testing.T) {
	cases := []struct {
		name string
		h    http.Header
		want string
	}{
		{"untagged", http.Header{}, ""},
		{"baggage only member", http.Header{"Baggage": {"branchbox-env=sb-42"}}, "sb-42"},
		{"baggage among others", http.Header{"Baggage": {"userId=alice, branchbox-env=sb-42;prop=1 ,isProduction=false"}}, "sb-42"},
		{"multiple baggage headers", http.Header{"Baggage": {"a=b", "branchbox-env=team-x"}}, "team-x"},
		{"explicit header wins", http.Header{"X-Branchbox-Env": {"hdr"}, "Baggage": {"branchbox-env=bag"}}, "hdr"},
		{"invalid id ignored", http.Header{"Baggage": {"branchbox-env=Bad_ID!"}}, ""},
		{"percent-encoded", http.Header{"Baggage": {"branchbox-env=sb%2D1"}}, "sb-1"},
	}
	for _, c := range cases {
		if got := FromHeaders(c.h); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestInjectPreservesOtherBaggage(t *testing.T) {
	h := http.Header{"Baggage": {"userId=alice,branchbox-env=old"}}
	Inject(h, "new")
	if got := h.Get("Baggage"); got != "branchbox-env=new,userId=alice" {
		t.Fatalf("baggage = %q", got)
	}
	if FromHeaders(h) != "new" {
		t.Fatal("injected env not readable")
	}
}

func TestEnsureTraceparent(t *testing.T) {
	h := http.Header{}
	id := EnsureTraceparent(h)
	if len(id) != 32 || TraceID(h) != id {
		t.Fatalf("new trace id %q, header %q", id, h.Get("Traceparent"))
	}
	if again := EnsureTraceparent(h); again != id {
		t.Fatal("existing trace context must be kept")
	}
}

func TestValid(t *testing.T) {
	for _, ok := range []string{"a", "sb-42", "e2e-sale", "x1234567890123456789012345678y"} {
		if !Valid(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "-a", "a-", "UPPER", "has.dot", "has_underscore", "x12345678901234567890123456789yz"} {
		if Valid(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
