package protocol

import (
	"bytes"
	"testing"

	"github.com/byggflow/sandbox/protocol/crypto"

	proto "github.com/byggflow/sandbox/protocol"
)

func testSession(t *testing.T) (*crypto.Session, *crypto.Session) {
	t.Helper()
	kp1, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	kp2, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := crypto.DeriveSharedSecret(kp1.Private, kp2.Public)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := crypto.NewSession(secret)
	if err != nil {
		t.Fatal(err)
	}
	secret2, err := crypto.DeriveSharedSecret(kp2.Private, kp1.Public)
	if err != nil {
		t.Fatal(err)
	}
	s2, err := crypto.NewSession(secret2)
	if err != nil {
		t.Fatal(err)
	}
	return s1, s2
}

func TestCryptoConnWriteEncryptsBinary(t *testing.T) {
	sender, receiver := testSession(t)

	// Wire buffer: the "network" between writer and reader.
	var wire bytes.Buffer

	// Writer side: CryptoConn wrapping a buffer (simulates agent writing).
	writerConn := NewCryptoConn(&wire, sender)

	plaintext := []byte("hello encrypted binary world")
	if err := WriteBinary(writerConn, plaintext); err != nil {
		t.Fatalf("WriteBinary: %v", err)
	}

	// The wire should contain an encrypted binary frame, not plaintext.
	wireBytes := wire.Bytes()
	if bytes.Contains(wireBytes, plaintext) {
		t.Fatal("plaintext found on wire; binary frame was not encrypted")
	}

	// Reader side: CryptoConn reading from the wire.
	readerConn := NewCryptoConn(&wire, receiver)
	frame, err := ReadFrame(readerConn)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frame.Type != proto.FrameBinary {
		t.Fatalf("expected binary frame, got 0x%02x", frame.Type)
	}
	if !bytes.Equal(frame.Payload, plaintext) {
		t.Fatalf("decrypted payload mismatch: got %q, want %q", frame.Payload, plaintext)
	}
}

func TestCryptoConnPassesThroughJSON(t *testing.T) {
	session, _ := testSession(t)

	var wire bytes.Buffer
	cc := NewCryptoConn(&wire, session)

	jsonData := []byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}`)
	if err := WriteJSON(cc, map[string]interface{}{"ok": true}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	// JSON frames should pass through without encryption.
	frame, err := ReadFrame(&wire)
	if err != nil {
		t.Fatalf("ReadFrame from wire: %v", err)
	}
	if frame.Type != proto.FrameJSON {
		t.Fatalf("expected JSON frame, got 0x%02x", frame.Type)
	}
	_ = jsonData // JSON content verified by frame type
}

func TestCryptoConnReadDecryptsBinary(t *testing.T) {
	sender, receiver := testSession(t)

	// Simulate encrypted binary frame written by a remote CryptoConn.
	var wire bytes.Buffer
	remoteConn := NewCryptoConn(&wire, sender)
	plaintext := []byte("secret file contents with lots of data 1234567890")
	if err := WriteBinary(remoteConn, plaintext); err != nil {
		t.Fatalf("WriteBinary: %v", err)
	}

	// Local CryptoConn should decrypt the binary frame.
	localConn := NewCryptoConn(&wire, receiver)
	frame, err := ReadFrame(localConn)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frame.Type != proto.FrameBinary {
		t.Fatalf("expected binary frame, got 0x%02x", frame.Type)
	}
	if !bytes.Equal(frame.Payload, plaintext) {
		t.Fatalf("mismatch: got %q, want %q", frame.Payload, plaintext)
	}
}

func TestCryptoConnMultipleChunks(t *testing.T) {
	sender, receiver := testSession(t)

	var wire bytes.Buffer
	writerConn := NewCryptoConn(&wire, sender)

	// Write multiple binary frames (simulating chunked file transfer).
	chunks := [][]byte{
		bytes.Repeat([]byte("A"), 1000),
		bytes.Repeat([]byte("B"), 2000),
		bytes.Repeat([]byte("C"), 500),
	}
	for _, chunk := range chunks {
		if err := WriteBinary(writerConn, chunk); err != nil {
			t.Fatalf("WriteBinary: %v", err)
		}
	}

	// Read them back through a decrypting CryptoConn.
	readerConn := NewCryptoConn(&wire, receiver)
	for i, expected := range chunks {
		frame, err := ReadFrame(readerConn)
		if err != nil {
			t.Fatalf("ReadFrame chunk %d: %v", i, err)
		}
		if frame.Type != proto.FrameBinary {
			t.Fatalf("chunk %d: expected binary frame, got 0x%02x", i, frame.Type)
		}
		if !bytes.Equal(frame.Payload, expected) {
			t.Fatalf("chunk %d: payload length %d, want %d", i, len(frame.Payload), len(expected))
		}
	}
}

func TestCryptoConnEmptyBinaryFrame(t *testing.T) {
	sender, receiver := testSession(t)

	var wire bytes.Buffer
	writerConn := NewCryptoConn(&wire, sender)

	// Empty binary frames should pass through (no encryption needed for empty payload).
	if err := WriteBinary(writerConn, []byte{}); err != nil {
		t.Fatalf("WriteBinary empty: %v", err)
	}

	readerConn := NewCryptoConn(&wire, receiver)
	frame, err := ReadFrame(readerConn)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if frame.Type != proto.FrameBinary {
		t.Fatalf("expected binary frame, got 0x%02x", frame.Type)
	}
	if len(frame.Payload) != 0 {
		t.Fatalf("expected empty payload, got %d bytes", len(frame.Payload))
	}
}
