//go:build linux

package main

import (
	"fmt"
	"net"
)

// vmaddrCIDHost is the vsock CID for the host kernel. Connections from
// any other CID — most notably VMADDR_CID_LOCAL (1) for in-guest
// loopback — must be rejected so a sandbox process can't impersonate
// the daemon to its own agent.
const vmaddrCIDHost uint32 = 2

// hostOnlyVsockListener wraps a vsock listener so it only accepts
// connections originating from VMADDR_CID_HOST. An in-sandbox process
// that dials vsock:VMADDR_CID_LOCAL:9111 would otherwise reach the
// agent's listen socket and try to authenticate.
func hostOnlyVsockListener(inner net.Listener) net.Listener {
	return &filteredListener{
		inner: inner,
		allow: func(c net.Conn) (bool, string) {
			va, ok := c.RemoteAddr().(*vsockAddr)
			if !ok {
				return false, "non-vsock peer on vsock listener"
			}
			if va.CID() != vmaddrCIDHost {
				return false, fmt.Sprintf("peer cid %d != host cid %d", va.CID(), vmaddrCIDHost)
			}
			return true, ""
		},
	}
}
