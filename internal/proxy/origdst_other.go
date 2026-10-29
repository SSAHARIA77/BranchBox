//go:build !linux

package proxy

import (
	"errors"
	"net"
	"net/netip"
)

// originalDst is only implemented on Linux, where the sidecar runs. This
// stub keeps the package compiling on developer machines (Windows/macOS).
func originalDst(*net.TCPConn) (netip.AddrPort, error) {
	return netip.AddrPort{}, errors.New("transparent interception requires Linux")
}
