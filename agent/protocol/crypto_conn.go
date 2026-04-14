package protocol

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/byggflow/sandbox/protocol/crypto"

	proto "github.com/byggflow/sandbox/protocol"
)

// CryptoConn wraps an io.ReadWriter to transparently encrypt outgoing binary
// frame payloads and decrypt incoming binary frame payloads using an E2E
// crypto session. JSON and ping frames pass through unmodified.
//
// This allows conn-based handlers (fs.Read, fs.Write, etc.) to operate on
// plaintext data while the wire carries encrypted binary frames.
type CryptoConn struct {
	inner   io.ReadWriter
	session *crypto.Session

	// rbuf holds a reassembled (possibly decrypted) frame ready to be served
	// to the caller via Read.
	rbuf bytes.Buffer

	// wbuf accumulates bytes from Write calls until a complete frame
	// (header + payload) is available for processing.
	wbuf bytes.Buffer
}

// NewCryptoConn creates a CryptoConn that encrypts/decrypts binary frames.
func NewCryptoConn(rw io.ReadWriter, session *crypto.Session) *CryptoConn {
	return &CryptoConn{inner: rw, session: session}
}

// Read implements io.Reader. It reads one frame at a time from the inner
// reader, decrypts binary frame payloads, and serves the resulting frame
// bytes (header + payload) to the caller.
func (c *CryptoConn) Read(p []byte) (int, error) {
	// Serve buffered data first.
	if c.rbuf.Len() > 0 {
		return c.rbuf.Read(p)
	}

	// Read one complete frame from the inner reader.
	header := make([]byte, 5)
	if _, err := io.ReadFull(c.inner, header); err != nil {
		return 0, err
	}

	ftype := header[0]
	length := binary.BigEndian.Uint32(header[1:5])

	if length > proto.MaxFrameSize {
		return 0, fmt.Errorf("frame size %d exceeds max %d", length, proto.MaxFrameSize)
	}

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(c.inner, payload); err != nil {
			return 0, err
		}
	}

	if ftype == proto.FrameBinary && length > 0 {
		decrypted, err := c.session.Open(payload)
		if err != nil {
			return 0, fmt.Errorf("decrypt binary frame: %w", err)
		}
		// Write a new frame header with the decrypted payload length.
		var hdr [5]byte
		hdr[0] = ftype
		binary.BigEndian.PutUint32(hdr[1:5], uint32(len(decrypted)))
		c.rbuf.Write(hdr[:])
		c.rbuf.Write(decrypted)
	} else {
		// Pass non-binary frames through unchanged.
		c.rbuf.Write(header)
		c.rbuf.Write(payload)
	}

	return c.rbuf.Read(p)
}

// Write implements io.Writer. It buffers incoming bytes (from WriteFrame
// calls), detects complete frames, encrypts binary frame payloads, and
// writes the encrypted frames to the inner writer.
func (c *CryptoConn) Write(p []byte) (int, error) {
	n := len(p)
	c.wbuf.Write(p)

	for c.wbuf.Len() >= 5 {
		buf := c.wbuf.Bytes()
		ftype := buf[0]
		length := binary.BigEndian.Uint32(buf[1:5])

		frameTotal := 5 + int(length)
		if c.wbuf.Len() < frameTotal {
			break // Incomplete frame, wait for more data.
		}

		// Extract the complete frame from the buffer.
		frame := make([]byte, frameTotal)
		c.wbuf.Read(frame)

		if ftype == proto.FrameBinary && length > 0 {
			payload := frame[5:]
			encrypted, err := c.session.Seal(payload)
			if err != nil {
				return 0, fmt.Errorf("encrypt binary frame: %w", err)
			}
			var hdr [5]byte
			hdr[0] = ftype
			binary.BigEndian.PutUint32(hdr[1:5], uint32(len(encrypted)))
			if _, err := c.inner.Write(hdr[:]); err != nil {
				return 0, err
			}
			if _, err := c.inner.Write(encrypted); err != nil {
				return 0, err
			}
		} else {
			// Pass non-binary frames through unchanged.
			if _, err := c.inner.Write(frame); err != nil {
				return 0, err
			}
		}
	}

	return n, nil
}
