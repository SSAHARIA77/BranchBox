//go:build linux

package proxy

import (
	"errors"
	"net"
	"net/netip"
	"syscall"
)

// soOriginalDst is the Linux socket option that returns the pre-NAT
// destination of a connection redirected by an iptables REDIRECT rule —
// the same primitive Istio/Envoy use for transparent interception.
const soOriginalDst = 80

// originalDst recovers the address the application originally dialled
// before iptables redirected the connection to the sidecar.
func originalDst(c *net.TCPConn) (netip.AddrPort, error) {
	rc, err := c.SyscallConn()
	if err != nil {
		return netip.AddrPort{}, err
	}
	var (
		mreq   *syscall.IPv6Mreq
		optErr error
	)
	// RawConn.Control runs on the live fd without switching the socket to
	// blocking mode (unlike TCPConn.File), so the conn stays usable.
	err = rc.Control(func(fd uintptr) {
		// A sockaddr_in fits inside the 16-byte IPv6Mreq buffer, which is
		// the standard trick for reading SO_ORIGINAL_DST from Go.
		mreq, optErr = syscall.GetsockoptIPv6Mreq(int(fd), syscall.IPPROTO_IP, soOriginalDst)
	})
	if err != nil {
		return netip.AddrPort{}, err
	}
	if optErr != nil {
		return netip.AddrPort{}, optErr
	}
	b := mreq.Multiaddr
	// sockaddr_in layout: family(2) | port(2, big-endian) | addr(4) | pad.
	port := uint16(b[2])<<8 | uint16(b[3])
	addr := netip.AddrFrom4([4]byte{b[4], b[5], b[6], b[7]})
	if !addr.IsValid() || port == 0 {
		return netip.AddrPort{}, errors.New("no original destination (connection was not redirected)")
	}
	return netip.AddrPortFrom(addr, port), nil
}
