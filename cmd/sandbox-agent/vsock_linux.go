//go:build linux

package main

import (
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"golang.org/x/sys/unix"
)

// listenVsock creates a vsock listener on the given port using AF_VSOCK.
func listenVsock(port string) (net.Listener, error) {
	p, err := strconv.Atoi(port)
	if err != nil {
		return nil, fmt.Errorf("invalid port %q: %w", port, err)
	}

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("create vsock socket: %w", err)
	}

	sa := &unix.SockaddrVM{
		CID:  unix.VMADDR_CID_ANY,
		Port: uint32(p),
	}
	if err := unix.Bind(fd, sa); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("bind vsock port %d: %w", p, err)
	}

	if err := unix.Listen(fd, 128); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("listen vsock: %w", err)
	}

	return &vsockListener{fd: fd, port: uint32(p)}, nil
}

// vsockListener implements net.Listener for AF_VSOCK sockets.
type vsockListener struct {
	fd   int
	port uint32
}

func (l *vsockListener) Accept() (net.Conn, error) {
	nfd, sa, err := unix.Accept(l.fd)
	if err != nil {
		return nil, fmt.Errorf("vsock accept: %w", err)
	}

	remoteCID := uint32(0)
	remotePort := uint32(0)
	if vsa, ok := sa.(*unix.SockaddrVM); ok {
		remoteCID = vsa.CID
		remotePort = vsa.Port
	}

	return &vsockConn{
		fd:         nfd,
		localPort:  l.port,
		remoteCID:  remoteCID,
		remotePort: remotePort,
	}, nil
}

func (l *vsockListener) Close() error {
	return unix.Close(l.fd)
}

func (l *vsockListener) Addr() net.Addr {
	return &vsockAddr{cid: unix.VMADDR_CID_ANY, port: l.port}
}

// vsockConn implements net.Conn for AF_VSOCK connections.
type vsockConn struct {
	fd         int
	localPort  uint32
	remoteCID  uint32
	remotePort uint32
}

func (c *vsockConn) Read(b []byte) (int, error) {
	n, err := unix.Read(c.fd, b)
	if n == 0 && err == nil {
		return 0, io.EOF
	}
	return n, err
}

func (c *vsockConn) Write(b []byte) (int, error) {
	return unix.Write(c.fd, b)
}

func (c *vsockConn) Close() error {
	return unix.Close(c.fd)
}

func (c *vsockConn) LocalAddr() net.Addr {
	return &vsockAddr{cid: unix.VMADDR_CID_ANY, port: c.localPort}
}

func (c *vsockConn) RemoteAddr() net.Addr {
	return &vsockAddr{cid: c.remoteCID, port: c.remotePort}
}

func (c *vsockConn) SetDeadline(t time.Time) error {
	return c.setDeadline(t)
}

func (c *vsockConn) SetReadDeadline(t time.Time) error {
	return c.setDeadline(t)
}

func (c *vsockConn) SetWriteDeadline(t time.Time) error {
	return c.setDeadline(t)
}

func (c *vsockConn) setDeadline(t time.Time) error {
	var tv unix.Timeval
	if !t.IsZero() {
		d := time.Until(t)
		if d <= 0 {
			d = time.Microsecond
		}
		tv = unix.NsecToTimeval(d.Nanoseconds())
	}
	if err := unix.SetsockoptTimeval(c.fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &tv); err != nil {
		return err
	}
	return unix.SetsockoptTimeval(c.fd, unix.SOL_SOCKET, unix.SO_SNDTIMEO, &tv)
}

// vsockAddr implements net.Addr for vsock.
type vsockAddr struct {
	cid  uint32
	port uint32
}

func (a *vsockAddr) Network() string { return "vsock" }
func (a *vsockAddr) String() string {
	return fmt.Sprintf("vsock(%d:%d)", a.cid, a.port)
}

