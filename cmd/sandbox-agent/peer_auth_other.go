//go:build !linux

package main

import "net"

// hostOnlyVsockListener stub for non-Linux. vsock isn't available on
// these platforms (listenVsock returns an error), so this is never
// invoked at runtime — exists only to keep the build green.
func hostOnlyVsockListener(inner net.Listener) net.Listener { return inner }
