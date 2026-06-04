package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/byggflow/sandbox/agent/egressproxy"
	proto "github.com/byggflow/sandbox/protocol"
)

// TestInstallCAWritesFilesAndEnv verifies that the OpNetCAInstall handler
// writes the cert file, builds a bundle, and exports the standard
// trust-bundle env vars to child processes.
func TestInstallCAWritesFilesAndEnv(t *testing.T) {
	// Skip if /tmp is not writable (e.g. unusual sandbox), and clean up
	// any artifacts we create.
	if _, err := os.Stat("/tmp"); err != nil {
		t.Skipf("/tmp unavailable: %v", err)
	}
	t.Cleanup(func() {
		os.Remove(egressproxy.CAPath)
		os.Remove(egressproxy.CABundlePath)
		for _, k := range []string{"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO"} {
			os.Unsetenv(k)
		}
	})

	pem := "-----BEGIN CERTIFICATE-----\nMIIBhello\n-----END CERTIFICATE-----\n"
	params, _ := json.Marshal(proto.CAInstallRequest{CertPEM: pem})

	result, err := installCA(params)
	if err != nil {
		t.Fatalf("installCA: %v", err)
	}
	m, ok := result.(map[string]interface{})
	if !ok || m["installed"] != true {
		t.Errorf("expected installed=true result, got %v", result)
	}

	got, err := os.ReadFile(egressproxy.CAPath)
	if err != nil {
		t.Fatalf("read %s: %v", egressproxy.CAPath, err)
	}
	if !strings.Contains(string(got), "MIIBhello") {
		t.Errorf("CA file does not contain expected PEM")
	}

	if got := os.Getenv("NODE_EXTRA_CA_CERTS"); got != egressproxy.CAPath {
		t.Errorf("NODE_EXTRA_CA_CERTS: got %q want %q", got, egressproxy.CAPath)
	}
	if got := os.Getenv("REQUESTS_CA_BUNDLE"); got != egressproxy.CABundlePath {
		t.Errorf("REQUESTS_CA_BUNDLE: got %q want %q", got, egressproxy.CABundlePath)
	}
}

func TestInstallCARejectsEmpty(t *testing.T) {
	params, _ := json.Marshal(proto.CAInstallRequest{CertPEM: ""})
	if _, err := installCA(params); err == nil {
		t.Error("expected error for empty cert pem")
	}
}
