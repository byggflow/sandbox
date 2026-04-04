package sandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/byggflow/sandbox/protocol/crypto"
)

// negotiateE2E performs the X25519 key exchange with the agent and returns
// a crypto session for encrypting/decrypting payloads.
func negotiateE2E(ctx context.Context, transport RpcTransport) (*crypto.Session, error) {
	// Generate client keypair.
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}

	// Send our public key to the agent.
	result, err := transport.Call(ctx, "session.negotiate_e2e", map[string]string{
		"public_key": base64.StdEncoding.EncodeToString(kp.Public.Bytes()),
	})
	if err != nil {
		return nil, fmt.Errorf("negotiate e2e: %w", err)
	}

	// Parse agent's public key from response.
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected negotiate response type")
	}
	agentPubB64, ok := resultMap["public_key"].(string)
	if !ok {
		return nil, fmt.Errorf("missing public_key in negotiate response")
	}

	agentPubBytes, err := base64.StdEncoding.DecodeString(agentPubB64)
	if err != nil {
		return nil, fmt.Errorf("decode agent public key: %w", err)
	}

	agentPub, err := crypto.PublicKeyFromBytes(agentPubBytes)
	if err != nil {
		return nil, fmt.Errorf("parse agent public key: %w", err)
	}

	// Derive shared secret.
	secret, err := crypto.DeriveSharedSecret(kp.Private, agentPub)
	if err != nil {
		return nil, fmt.Errorf("derive shared secret: %w", err)
	}

	return crypto.NewSession(secret)
}

// encryptedTransport wraps an RpcTransport and encrypts/decrypts payloads
// using the negotiated E2E crypto session.
type encryptedTransport struct {
	inner   RpcTransport
	session *crypto.Session
}

func (t *encryptedTransport) Call(ctx context.Context, method string, params interface{}) (interface{}, error) {
	// Encrypt params.
	encrypted, err := t.encryptParams(params)
	if err != nil {
		return nil, fmt.Errorf("e2e encrypt params: %w", err)
	}

	result, err := t.inner.Call(ctx, method, encrypted)
	if err != nil {
		return nil, err
	}

	// Decrypt result.
	return t.decryptResult(result)
}

func (t *encryptedTransport) Notify(ctx context.Context, method string, params interface{}) error {
	encrypted, err := t.encryptParams(params)
	if err != nil {
		return fmt.Errorf("e2e encrypt params: %w", err)
	}
	return t.inner.Notify(ctx, method, encrypted)
}

func (t *encryptedTransport) OnNotification(handler NotificationHandler) {
	t.inner.OnNotification(func(method string, params interface{}) {
		decrypted, err := t.decryptResult(params)
		if err != nil {
			handler(method, params) // Fall back to raw if decryption fails.
			return
		}
		handler(method, decrypted)
	})
}

func (t *encryptedTransport) OnReplaced(handler ReplacedHandler) {
	t.inner.OnReplaced(handler)
}

func (t *encryptedTransport) Close() error {
	return t.inner.Close()
}

func (t *encryptedTransport) encryptParams(params interface{}) (map[string]string, error) {
	plaintext, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	encrypted, err := t.session.SealBase64(plaintext)
	if err != nil {
		return nil, err
	}
	return map[string]string{"_encrypted": encrypted}, nil
}

func (t *encryptedTransport) decryptResult(result interface{}) (interface{}, error) {
	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return result, nil // Not encrypted.
	}
	encrypted, ok := resultMap["_encrypted"].(string)
	if !ok {
		return result, nil // No _encrypted field.
	}
	plaintext, err := t.session.OpenBase64(encrypted)
	if err != nil {
		return nil, fmt.Errorf("e2e decrypt: %w", err)
	}
	var decoded interface{}
	if err := json.Unmarshal(plaintext, &decoded); err != nil {
		return nil, fmt.Errorf("unmarshal decrypted result: %w", err)
	}
	return decoded, nil
}
