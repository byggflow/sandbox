package crypto

import (
	"bytes"
	"testing"
)

func TestKeyExchangeAndEncryption(t *testing.T) {
	// Simulate client and agent generating key pairs.
	clientKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("client keygen: %v", err)
	}
	agentKP, err := GenerateKeyPair()
	if err != nil {
		t.Fatalf("agent keygen: %v", err)
	}

	// Both sides derive the same shared secret.
	clientSecret, err := DeriveSharedSecret(clientKP.Private, agentKP.Public)
	if err != nil {
		t.Fatalf("client derive: %v", err)
	}
	agentSecret, err := DeriveSharedSecret(agentKP.Private, clientKP.Public)
	if err != nil {
		t.Fatalf("agent derive: %v", err)
	}

	if !bytes.Equal(clientSecret, agentSecret) {
		t.Fatal("shared secrets do not match")
	}

	// Create sessions.
	clientSession, err := NewSession(clientSecret)
	if err != nil {
		t.Fatalf("client session: %v", err)
	}
	agentSession, err := NewSession(agentSecret)
	if err != nil {
		t.Fatalf("agent session: %v", err)
	}

	// Client encrypts, agent decrypts.
	plaintext := []byte(`{"command":"echo secret","env":{"API_KEY":"sk-12345"}}`)
	ciphertext, err := clientSession.Seal(plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	// Ciphertext should differ from plaintext.
	if bytes.Equal(ciphertext, plaintext) {
		t.Fatal("ciphertext equals plaintext")
	}

	decrypted, err := agentSession.Open(ciphertext)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	if !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("decrypted != plaintext: %q vs %q", decrypted, plaintext)
	}
}

func TestBase64RoundTrip(t *testing.T) {
	kp1, _ := GenerateKeyPair()
	kp2, _ := GenerateKeyPair()
	secret, _ := DeriveSharedSecret(kp1.Private, kp2.Public)

	session, err := NewSession(secret)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}

	plaintext := []byte("hello encrypted world")
	encoded, err := session.SealBase64(plaintext)
	if err != nil {
		t.Fatalf("seal base64: %v", err)
	}

	decoded, err := session.OpenBase64(encoded)
	if err != nil {
		t.Fatalf("open base64: %v", err)
	}

	if !bytes.Equal(decoded, plaintext) {
		t.Fatalf("mismatch: %q vs %q", decoded, plaintext)
	}
}

func TestPublicKeyFromBytes(t *testing.T) {
	kp, _ := GenerateKeyPair()
	raw := kp.Public.Bytes()

	pub, err := PublicKeyFromBytes(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if !bytes.Equal(pub.Bytes(), kp.Public.Bytes()) {
		t.Fatal("public key mismatch")
	}
}

func TestTamperedCiphertext(t *testing.T) {
	kp1, _ := GenerateKeyPair()
	kp2, _ := GenerateKeyPair()
	secret, _ := DeriveSharedSecret(kp1.Private, kp2.Public)
	session, _ := NewSession(secret)

	ct, _ := session.Seal([]byte("sensitive data"))
	// Flip a byte.
	ct[len(ct)-1] ^= 0xff

	_, err := session.Open(ct)
	if err == nil {
		t.Fatal("expected error on tampered ciphertext")
	}
}

func TestInvalidSecretLength(t *testing.T) {
	_, err := NewSession([]byte("tooshort"))
	if err == nil {
		t.Fatal("expected error for short secret")
	}
}
