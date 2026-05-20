// Package egressproxy is the agent's in-sandbox HTTP proxy. It accepts
// HTTP requests from sandbox processes (selected via HTTP_PROXY env var)
// and forwards each one to the daemon as a protocol.OpNetEgress RPC.
//
// The daemon applies the registered rules (inject headers, allow, deny)
// and dials the actual upstream; the response is streamed back through
// this proxy to the requesting client.
//
// Credentials live only on the daemon. The sandbox process never observes
// the headers added by inject rules.
package egressproxy

import (
	"bufio"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/byggflow/sandbox/agent/phonehome"
	"github.com/byggflow/sandbox/protocol"
)

// Server is the agent's local HTTP forward-proxy.
type Server struct {
	addr    string
	client  atomic.Pointer[phonehome.Client]
	log     *slog.Logger
	streams *streamRegistry

	ln       net.Listener
	httpSrv  *http.Server
	stopOnce sync.Once
}

// New constructs a Server bound to addr (e.g. "127.0.0.1:8118"). The
// phonehome client is set per-connection by the agent server via
// SetClient so reconnects swap in a fresh writer.
func New(addr string, log *slog.Logger) *Server {
	return &Server{addr: addr, log: log, streams: newStreamRegistry()}
}

// DeliverStreamData routes an inbound FrameStreamData payload to the
// matching stream. Returns false if no stream is registered.
func (s *Server) DeliverStreamData(streamID uint32, data []byte) bool {
	// Copy data because the caller may reuse the underlying buffer.
	buf := make([]byte, len(data))
	copy(buf, data)
	return s.streams.deliver(streamID, streamChunk{data: buf})
}

// DeliverStreamEnd routes an inbound FrameStreamEnd payload.
func (s *Server) DeliverStreamEnd(streamID uint32, status byte, errMsg string) bool {
	chunk := streamChunk{end: true}
	if status != 0 {
		chunk.errMsg = errMsg
		if chunk.errMsg == "" {
			chunk.errMsg = "stream ended with error"
		}
	}
	return s.streams.deliver(streamID, chunk)
}

// CloseStreams tears down any in-flight streams. Called from the agent
// server when a daemon connection drops.
func (s *Server) CloseStreams() {
	s.streams.closeAll()
}

// CloseStreamsForClient tears down only streams allocated through c.
// Called when an old daemon connection exits after a reconnect has
// already installed a newer client.
func (s *Server) CloseStreamsForClient(c *phonehome.Client) {
	s.streams.closeAllForOwner(c)
}

// SetClient swaps in the phonehome client the proxy uses for dispatch.
// Called from the agent server on each new daemon connection so the
// proxy always writes to the current authenticated channel.
func (s *Server) SetClient(c *phonehome.Client) {
	s.client.Store(c)
}

// ClearClientIf nil-stores the current client ONLY if it still equals
// `expected`. Returns true when the swap happened. Used by handleConn's
// teardown defer so a new connection's SetClient isn't stomped by the
// previous connection's defer firing after reconnect/session-replace.
//
// Without this, the sequence: old conn installs client A → new conn
// installs client B → old conn's defer fires and unconditionally
// SetClient(nil) → egress proxy now has no client, all sandbox HTTP
// fails with "no active daemon connection" until another reconnect.
func (s *Server) ClearClientIf(expected *phonehome.Client) bool {
	return s.client.CompareAndSwap(expected, nil)
}

// keepAliveReadTimeout bounds how long the sandbox client may stall
// between requests on a keep-alive CONNECT tunnel. Matches the order
// of http.Server.ReadHeaderTimeout we set on the outer listener so a
// stalled sandbox client can't hold a goroutine open indefinitely.
const keepAliveReadTimeout = 30 * time.Second

// chunkWriteTimeout bounds how long a single chunk write to the
// sandbox client can stall before the handler gives up. Long enough
// for normal TCP backpressure on slow downstreams, short enough that
// a non-reading client can't pin the goroutine + stream-channel and
// indirectly stall the agent's read loop.
const chunkWriteTimeout = 30 * time.Second

// deadlineWriter resets the underlying conn's write deadline on every
// Write so a single stalled write can't block forever — without
// capping total stream duration. Used for the TLS chunked response in
// the CONNECT keep-alive loop.
type deadlineWriter struct {
	conn    net.Conn
	w       io.Writer
	timeout time.Duration
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	if err := d.conn.SetWriteDeadline(time.Now().Add(d.timeout)); err != nil {
		return 0, err
	}
	return d.w.Write(p)
}

// tlsHandshakeTimeout bounds the TLS handshake the agent performs
// with the sandbox client after CONNECT. Hijacking the conn removes
// the http.Server's context-driven cancellation, so without this a
// stalled client could pin a goroutine + FD forever.
const tlsHandshakeTimeout = 15 * time.Second

// ListenAndServe binds the listener and serves until Close is called. It is
// non-blocking: returns once the listener is bound and the accept loop has
// started.
func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("egressproxy listen %s: %w", s.addr, err)
	}
	s.ln = ln

	s.httpSrv = &http.Server{
		Handler:           http.HandlerFunc(s.serve),
		ReadHeaderTimeout: 30 * time.Second,
		// HTTP CONNECT for HTTPS is handled inside serve via Hijacker.
	}
	go func() {
		if err := s.httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("egressproxy serve", "error", err)
		}
	}()
	s.log.Info("egress proxy listening", "addr", s.ln.Addr().String())
	return nil
}

// Addr returns the local listen address.
func (s *Server) Addr() string {
	if s.ln == nil {
		return s.addr
	}
	return s.ln.Addr().String()
}

// Close stops the proxy.
func (s *Server) Close() error {
	var err error
	s.stopOnce.Do(func() {
		if s.httpSrv != nil {
			err = s.httpSrv.Close()
		}
	})
	return err
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.handleConnect(w, r)
		return
	}
	s.handlePlain(w, r)
}

// handlePlain forwards an absolute-URL HTTP request (the form clients send
// when HTTP_PROXY is set) as an OpNetEgressStream RPC. The response body
// is streamed back chunk-by-chunk so SSE / chunked transfer / large
// downloads work end-to-end.
func (s *Server) handlePlain(w http.ResponseWriter, r *http.Request) {
	if !r.URL.IsAbs() {
		http.Error(w, "absolute URL required for forward-proxy", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, protocol.MaxEgressBody+1))
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if len(body) > protocol.MaxEgressBody {
		http.Error(w, "request body exceeds maximum size", http.StatusRequestEntityTooLarge)
		return
	}

	headers := copyHeaders(r.Header)
	req := &protocol.EgressRequest{
		Method:  r.Method,
		URL:     r.URL.String(),
		Headers: headers,
		Body:    base64.StdEncoding.EncodeToString(body),
	}

	head, ch, release, err := s.dispatchStream(req)
	if err != nil {
		http.Error(w, "egress dispatch: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer release()

	for k, vs := range head.Headers {
		if isHopByHop(k) {
			continue
		}
		// Strip Content-Length / Transfer-Encoding: we stream the body
		// chunk-by-chunk and net/http picks the right framing on its
		// own. Leaving the upstream's Content-Length in place forces
		// fixed-length mode — and any rule that rewrites or truncates
		// the body then hangs (client waits for the announced bytes)
		// or silently drops (server stops writing at the announced
		// length). The TLS CONNECT path already strips these; this
		// matches the same contract on the plain HTTP path.
		lk := strings.ToLower(k)
		if lk == "content-length" || lk == "transfer-encoding" {
			continue
		}
		w.Header()[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
	w.WriteHeader(head.Status)

	flusher, _ := w.(http.Flusher)
	if err := streamBodyTo(ch, w, flusher); err != nil {
		s.log.Debug("egress: stream body", "error", err)
		// We've already started writing the response (status + headers
		// + possibly some chunks). The only way to signal "this body
		// is truncated, do NOT treat as successful" to the sandbox
		// client is to break the underlying TCP connection. Panicking
		// with http.ErrAbortHandler is how net/http's docs say to do
		// this: the server suppresses the panic log and closes the
		// conn without writing a chunked terminator, so the sandbox
		// client sees an abnormal EOF rather than a clean response.
		panic(http.ErrAbortHandler)
	}
}

// streamBodyTo reads chunks from ch and writes them to w, flushing each
// chunk so SSE clients see events as they arrive. Returns:
//   - nil only when the stream ends cleanly (terminal frame with
//     StreamEndOK, errMsg empty)
//   - the daemon-reported error when the terminal frame carries one
//   - errStreamClosedWithoutTerminal when the channel closes without
//     any terminal frame (closeAll timeout path — the response body
//     is truncated and we MUST signal this to the caller so it can
//     break the underlying conn rather than write a clean terminator)
func streamBodyTo(ch <-chan streamChunk, w io.Writer, flusher http.Flusher) error {
	for chunk := range ch {
		if chunk.end {
			if chunk.errMsg != "" {
				return errors.New(chunk.errMsg)
			}
			return nil
		}
		if _, err := w.Write(chunk.data); err != nil {
			return fmt.Errorf("writing chunk to client: %w", err)
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	// Channel closed without an end marker. This only happens via
	// closeAll's timeout fallback (daemon connection dropped and the
	// terminal frame didn't fit in the buffer in time). Treat as
	// truncation, not clean EOF.
	return errStreamClosedWithoutTerminal
}

// errStreamClosedWithoutTerminal indicates the stream channel closed
// before a terminal frame arrived. Callers MUST surface this as an
// abnormal termination — never a clean response — or they risk
// reporting a truncated body as successful.
var errStreamClosedWithoutTerminal = errors.New("egress stream closed without terminal marker (forced teardown)")

// copyHeaders converts http.Header into our wire-format header map,
// stripping hop-by-hop entries so the upstream never sees them.
func copyHeaders(in http.Header) map[string][]string {
	out := make(map[string][]string, len(in))
	for k, vs := range in {
		if isHopByHop(k) {
			continue
		}
		out[k] = append([]string(nil), vs...)
	}
	return out
}

// handleConnect MITMs an HTTPS CONNECT request. Steps:
//
//  1. Parse the target host:port.
//  2. Ask the daemon for a leaf cert via OpNetCertLeaf.
//  3. Reply "200 Connection Established" to the sandbox client.
//  4. Wrap the raw conn in a TLS server using the leaf cert.
//  5. Read one HTTP request from the decrypted stream.
//  6. Forward via the same dispatch() path used for plain HTTP.
//  7. Write the response back over TLS.
//
// The leaf private key lives in the agent's memory for the lifetime of the
// CONNECT. The CA private key never leaves the daemon.
func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	// r.Host is "host:port" for CONNECT. The host:port from the CONNECT
	// line is the authoritative destination — the inner Host header is
	// attacker-controllable and MUST NOT be allowed to redirect rule
	// matching to a different host.
	connectHost, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		http.Error(w, "invalid CONNECT target: "+err.Error(), http.StatusBadRequest)
		return
	}

	leaf, err := s.fetchLeaf(connectHost)
	if err != nil {
		s.log.Warn("egress: leaf cert request failed", "host", connectHost, "error", err)
		http.Error(w, "egress: leaf cert unavailable: "+err.Error(), http.StatusBadGateway)
		return
	}

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "egress: streaming not supported", http.StatusInternalServerError)
		return
	}
	clientConn, bufRW, err := hj.Hijack()
	if err != nil {
		http.Error(w, "egress: hijack failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer clientConn.Close()

	if _, err := bufRW.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		s.log.Warn("egress: write CONNECT ack", "error", err)
		return
	}
	if err := bufRW.Flush(); err != nil {
		s.log.Warn("egress: flush CONNECT ack", "error", err)
		return
	}

	// Hijack returns a bufio.ReadWriter that may have already buffered
	// bytes past the CONNECT line. tls.Server reads directly from the
	// raw conn, so any buffered bytes would be lost (and a pipelined TLS
	// ClientHello would never decrypt). Wrap the conn so reads drain
	// the buffered bytes first.
	connForTLS := drainingConn(clientConn, bufRW.Reader)

	tlsConn := tls.Server(connForTLS, &tls.Config{
		Certificates: []tls.Certificate{leaf},
		MinVersion:   tls.VersionTLS12,
	})
	// HandshakeContext only watches r.Context(), and once we Hijacked
	// the conn the http.Server stops cancelling that context — so a
	// sandbox client that opens CONNECT and then sits silent would
	// stall here indefinitely. Bound the handshake with a deadline on
	// the underlying conn.
	if err := clientConn.SetDeadline(time.Now().Add(tlsHandshakeTimeout)); err != nil {
		s.log.Debug("egress: set tls handshake deadline", "error", err)
		return
	}
	if err := tlsConn.HandshakeContext(r.Context()); err != nil {
		s.log.Warn("egress: tls handshake with sandbox", "host", connectHost, "error", err)
		return
	}
	if err := clientConn.SetDeadline(time.Time{}); err != nil {
		s.log.Debug("egress: clear tls handshake deadline", "error", err)
		return
	}
	defer tlsConn.Close()

	// Loop over keep-alive: read requests one at a time, dispatch each.
	// Each iteration bounds how long a misbehaving sandbox client can
	// hold the goroutine open by stalling mid-headers or mid-body.
	// Without a deadline, a client that opens CONNECT and then never
	// sends another byte keeps this goroutine alive indefinitely; the
	// outer ReadHeaderTimeout only covers the initial plain CONNECT.
	reader := bufio.NewReader(tlsConn)
	for {
		if err := clientConn.SetReadDeadline(time.Now().Add(keepAliveReadTimeout)); err != nil {
			s.log.Debug("egress: set tls read deadline", "error", err)
			return
		}
		req, err := http.ReadRequest(reader)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				s.log.Debug("egress: tls request read", "error", err)
			}
			return
		}

		body, err := io.ReadAll(io.LimitReader(req.Body, protocol.MaxEgressBody+1))
		req.Body.Close()
		// Clear the read deadline so the upstream dispatch (which may
		// stream a long response back) isn't aborted by the same
		// header-timeout. The next iteration re-arms it.
		_ = clientConn.SetReadDeadline(time.Time{})
		if err != nil {
			writeTLSError(tlsConn, http.StatusBadRequest, "read body: "+err.Error())
			return
		}
		if len(body) > protocol.MaxEgressBody {
			writeTLSError(tlsConn, http.StatusRequestEntityTooLarge, "request body exceeds maximum size")
			return
		}

		headers := copyHeaders(req.Header)

		// Always derive the URL host from the CONNECT target — the inner
		// Host header is sandbox-controlled and would let a malicious
		// client target a different host's rule set.
		target := url.URL{Scheme: "https", Host: r.Host, Path: req.URL.Path, RawQuery: req.URL.RawQuery}

		eReq := &protocol.EgressRequest{
			Method:  req.Method,
			URL:     target.String(),
			Headers: headers,
			Body:    base64.StdEncoding.EncodeToString(body),
		}

		head, ch, release, err := s.dispatchStream(eReq)
		if err != nil {
			writeTLSError(tlsConn, http.StatusBadGateway, "egress dispatch: "+err.Error())
			return
		}

		// Bump the write deadline on every write so a stalled sandbox
		// client can't pin tlsConn.Write indefinitely. Without this,
		// release() never fires, the stream channel fills, and the
		// agent's single read loop blocks delivering further frames —
		// stalling every other stream and ping on the daemon
		// connection.
		dw := &deadlineWriter{conn: clientConn, w: tlsConn, timeout: chunkWriteTimeout}
		if err := writeStreamTLSResponse(dw, head, ch); err != nil {
			s.log.Debug("egress: write tls response", "error", err)
			release()
			return
		}
		_ = clientConn.SetWriteDeadline(time.Time{})
		release()

		// Close on Connection: close or HTTP/1.0 without keep-alive.
		if req.Close || req.ProtoMajor < 1 || (req.ProtoMajor == 1 && req.ProtoMinor == 0) {
			return
		}
	}
}

// writeStreamTLSResponse emits the response headers immediately, then
// streams body chunks to the TLS conn using HTTP/1.1 chunked transfer
// encoding so the sandbox client sees each frame as it arrives.
func writeStreamTLSResponse(w io.Writer, head *protocol.StreamEgressResponse, ch <-chan streamChunk) error {
	bw := bufio.NewWriter(w)

	statusText := http.StatusText(head.Status)
	if statusText == "" {
		statusText = "Status"
	}
	if _, err := fmt.Fprintf(bw, "HTTP/1.1 %d %s\r\n", head.Status, statusText); err != nil {
		return err
	}
	for k, vs := range head.Headers {
		lk := strings.ToLower(k)
		if isHopByHop(lk) || lk == "content-length" || lk == "transfer-encoding" {
			continue
		}
		for _, v := range vs {
			if _, err := fmt.Fprintf(bw, "%s: %s\r\n", k, v); err != nil {
				return err
			}
		}
	}
	if _, err := bw.WriteString("Transfer-Encoding: chunked\r\n\r\n"); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}

	// Stream body as HTTP/1.1 chunks. Each non-empty chunk = "size\r\ndata\r\n".
	// Terminator = "0\r\n\r\n".
	cw := &httpChunkWriter{w: w}
	if err := streamBodyTo(ch, cw, nil); err != nil {
		return err
	}
	return cw.Close()
}

// httpChunkWriter encodes writes as HTTP/1.1 chunked transfer encoding
// segments, one chunk per Write call.
type httpChunkWriter struct {
	w io.Writer
}

func (c *httpChunkWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if _, err := fmt.Fprintf(c.w, "%x\r\n", len(p)); err != nil {
		return 0, err
	}
	n, err := c.w.Write(p)
	if err != nil {
		return n, err
	}
	if _, err := c.w.Write([]byte("\r\n")); err != nil {
		return n, err
	}
	return n, nil
}

func (c *httpChunkWriter) Close() error {
	_, err := c.w.Write([]byte("0\r\n\r\n"))
	return err
}

// fetchLeaf asks the daemon for a leaf cert + key for host, then assembles
// a tls.Certificate ready to present to the sandbox client.
func (s *Server) fetchLeaf(host string) (tls.Certificate, error) {
	c := s.client.Load()
	if c == nil {
		return tls.Certificate{}, errors.New("no active daemon connection")
	}
	resp, err := c.Call(protocol.OpNetCertLeaf, &protocol.LeafCertRequest{Host: host}, 10*time.Second)
	if err != nil {
		return tls.Certificate{}, err
	}
	if resp.Error != nil {
		return tls.Certificate{}, fmt.Errorf("daemon: %s", resp.Error.Message)
	}
	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal leaf result: %w", err)
	}
	var out protocol.LeafCertResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return tls.Certificate{}, fmt.Errorf("decode leaf result: %w", err)
	}
	cert, err := tls.X509KeyPair([]byte(out.CertPEM), []byte(out.KeyPEM))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse leaf pem: %w", err)
	}
	return cert, nil
}

// writeTLSResponse serializes an EgressResponse back over the TLS conn as
// a valid HTTP/1.1 response. Emits each header value on its own line so
// Set-Cookie and other repeated headers survive the round-trip.
func writeTLSResponse(w io.Writer, resp *protocol.EgressResponse) error {
	body, _ := base64.StdEncoding.DecodeString(resp.Body)

	bw := bufio.NewWriter(w)
	statusText := http.StatusText(resp.Status)
	if statusText == "" {
		statusText = "Status"
	}
	if _, err := fmt.Fprintf(bw, "HTTP/1.1 %d %s\r\n", resp.Status, statusText); err != nil {
		return err
	}
	for k, vs := range resp.Headers {
		lk := strings.ToLower(k)
		if isHopByHop(lk) || lk == "content-length" {
			continue
		}
		for _, v := range vs {
			if _, err := fmt.Fprintf(bw, "%s: %s\r\n", k, v); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintf(bw, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	if _, err := bw.Write(body); err != nil {
		return err
	}
	return bw.Flush()
}

func writeTLSError(w io.Writer, status int, msg string) {
	resp := &protocol.EgressResponse{
		Status:  status,
		Headers: map[string][]string{"Content-Type": {"text/plain; charset=utf-8"}},
		Body:    base64.StdEncoding.EncodeToString([]byte(msg)),
	}
	_ = writeTLSResponse(w, resp)
}

// drainingConn wraps c so that reads first drain any bytes already
// buffered in r (typically from a bufio.ReadWriter returned by Hijack),
// then fall through to c. Writes pass through unchanged.
func drainingConn(c net.Conn, r *bufio.Reader) net.Conn {
	if n := r.Buffered(); n == 0 {
		return c
	}
	return &drainConn{Conn: c, buf: r}
}

type drainConn struct {
	net.Conn
	buf *bufio.Reader
}

func (d *drainConn) Read(p []byte) (int, error) {
	if d.buf.Buffered() > 0 {
		return d.buf.Read(p)
	}
	return d.Conn.Read(p)
}

// dispatch sends the request to the daemon and decodes the response.
// Returns a synthetic 503-style error if no daemon connection is
// currently active.
func (s *Server) dispatch(req *protocol.EgressRequest) (*protocol.EgressResponse, error) {
	c := s.client.Load()
	if c == nil {
		return nil, errors.New("no active daemon connection")
	}
	resp, err := c.Call(protocol.OpNetEgress, req, 60*time.Second)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("daemon: %s", resp.Error.Message)
	}

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, fmt.Errorf("re-marshaling result: %w", err)
	}
	var out protocol.EgressResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decoding result: %w", err)
	}
	return &out, nil
}

// dispatchStream initiates a streaming egress request. Returns the
// upstream status+headers, plus a receive-only channel that yields
// body chunks as the daemon pushes them. The cleanup function MUST be
// called when the caller is done with the stream (success or error).
func (s *Server) dispatchStream(req *protocol.EgressRequest) (*protocol.StreamEgressResponse, <-chan streamChunk, func(), error) {
	c := s.client.Load()
	if c == nil {
		return nil, nil, func() {}, errors.New("no active daemon connection")
	}
	streamID, ch, release := s.streams.allocate(c)
	// On any error before we hand the stream to the caller, release.
	ok := false
	defer func() {
		if !ok {
			release()
		}
	}()

	resp, err := c.Call(protocol.OpNetEgressStream, &protocol.StreamEgressRequest{
		StreamID: streamID,
		Request:  *req,
	}, 60*time.Second)
	if err != nil {
		return nil, nil, nil, err
	}
	if resp.Error != nil {
		return nil, nil, nil, fmt.Errorf("daemon: %s", resp.Error.Message)
	}

	raw, err := json.Marshal(resp.Result)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("re-marshaling result: %w", err)
	}
	var head protocol.StreamEgressResponse
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, nil, nil, fmt.Errorf("decoding result: %w", err)
	}

	ok = true
	return &head, ch, release, nil
}

var hopByHop = map[string]struct{}{
	"connection":          {},
	"proxy-connection":    {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

func isHopByHop(h string) bool {
	_, ok := hopByHop[strings.ToLower(h)]
	return ok
}
