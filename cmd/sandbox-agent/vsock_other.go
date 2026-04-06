//go:build !linux

package main

import (
	"fmt"
	"net"
)

// listenVsock is not supported on non-Linux platforms.
func listenVsock(port string) (net.Listener, error) {
	return nil, fmt.Errorf("vsock is not supported on this platform")
}
