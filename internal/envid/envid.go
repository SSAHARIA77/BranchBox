// Package envid extracts and injects the BranchBox environment ID carried by
// a request.
//
// The environment ID travels as a W3C Baggage list-member
// ("baggage: branchbox-env=sb-42"). Baggage is chosen deliberately: any
// service instrumented with OpenTelemetry already forwards it on every
// outgoing call, so the ID survives every hop without the application
// knowing BranchBox exists. A plain X-Branchbox-Env header is also accepted
// for hand-written requests, but it only survives hops whose application
// forwards it explicitly.
package envid

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const (
	// BaggageKey is the W3C Baggage key that carries the environment ID.
	BaggageKey = "branchbox-env"
	// Header is the explicit header alternative to baggage.
	Header = "X-Branchbox-Env"
)

var validID = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`)

// Valid reports whether id can be used as an environment ID. IDs end up in
// container names and DNS labels, so they are restricted accordingly.
func Valid(id string) bool { return validID.MatchString(id) }

// FromHeaders returns the environment ID carried by h, or "" if the request
// is untagged (and must therefore be served by the baseline).
func FromHeaders(h http.Header) string {
	if v := strings.TrimSpace(h.Get(Header)); Valid(v) {
		return v
	}
	if v := fromBaggage(h.Values("Baggage")); Valid(v) {
		return v
	}
	return ""
}

// fromBaggage parses W3C Baggage header values
// ("k1=v1;prop, k2=v2") and returns the value of BaggageKey.
func fromBaggage(values []string) string {
	for _, header := range values {
		for member := range strings.SplitSeq(header, ",") {
			kv, _, _ := strings.Cut(member, ";")
			k, v, ok := strings.Cut(kv, "=")
			if !ok || strings.TrimSpace(k) != BaggageKey {
				continue
			}
			v = strings.TrimSpace(v)
			if dec, err := url.PathUnescape(v); err == nil {
				v = dec
			}
			return v
		}
	}
	return ""
}

// Inject tags h with env, replacing any existing BranchBox entry while
// keeping every other baggage member intact.
func Inject(h http.Header, env string) {
	h.Set(Header, env)
	members := []string{BaggageKey + "=" + env}
	for _, header := range h.Values("Baggage") {
		for member := range strings.SplitSeq(header, ",") {
			member = strings.TrimSpace(member)
			k, _, _ := strings.Cut(member, "=")
			if member == "" || strings.TrimSpace(k) == BaggageKey {
				continue
			}
			members = append(members, member)
		}
	}
	h.Set("Baggage", strings.Join(members, ","))
}

// TraceID returns the trace ID from a W3C traceparent header, or "".
func TraceID(h http.Header) string {
	parts := strings.Split(strings.TrimSpace(h.Get("Traceparent")), "-")
	if len(parts) < 4 || len(parts[1]) != 32 {
		return ""
	}
	return parts[1]
}

// EnsureTraceparent starts a new sampled W3C trace on h if it does not
// already carry one, and returns the trace ID in effect. This lets
// BranchBox correlate every hop of a single request even when the caller
// (for example a browser) sent no trace context.
func EnsureTraceparent(h http.Header) string {
	if id := TraceID(h); id != "" {
		return id
	}
	traceID, spanID := randomHex(16), randomHex(8)
	h.Set("Traceparent", "00-"+traceID+"-"+spanID+"-01")
	return traceID
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
