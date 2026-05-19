package egressproxy

import (
	"fmt"
	"io"
	"log/slog"
	"os"
)

// CAPath is the disk location where the per-sandbox CA is written.
// Exported so user processes (and the integration suite) can reference it.
const CAPath = "/tmp/sandbox-ca.crt"

// CABundlePath is the merged system+sandbox trust bundle.
const CABundlePath = "/tmp/sandbox-ca-bundle.crt"

// WriteCAFiles writes the given CA PEM to /tmp/sandbox-ca.crt and writes
// a merged trust bundle (system CAs + this sandbox's CA) to
// /tmp/sandbox-ca-bundle.crt. Called from the agent's OpNetCAInstall
// handler when the daemon pushes the CA during readiness check.
//
// /tmp/sandbox-ca.crt        is for clients that accept ADDITIONAL CAs
//                            (Node via NODE_EXTRA_CA_CERTS).
// /tmp/sandbox-ca-bundle.crt is for clients that REPLACE the trust store
//                            (REQUESTS_CA_BUNDLE, SSL_CERT_FILE,
//                            CURL_CA_BUNDLE, GIT_SSL_CAINFO).
func WriteCAFiles(pem string) error {
	if pem == "" {
		return fmt.Errorf("empty ca pem")
	}

	if err := os.WriteFile(CAPath, []byte(pem), 0o644); err != nil {
		return fmt.Errorf("writing sandbox-ca.crt: %w", err)
	}

	merged, err := os.Create(CABundlePath)
	if err != nil {
		return fmt.Errorf("creating sandbox-ca-bundle.crt: %w", err)
	}
	defer merged.Close()

	for _, src := range systemBundleCandidates {
		f, err := os.Open(src)
		if err != nil {
			continue
		}
		if _, err := io.Copy(merged, f); err != nil {
			f.Close()
			slog.Warn("egress ca: copying system bundle", "src", src, "error", err)
			continue
		}
		f.Close()
		break
	}
	if _, err := merged.WriteString("\n"); err != nil {
		return fmt.Errorf("writing bundle separator: %w", err)
	}
	if _, err := merged.WriteString(pem); err != nil {
		return fmt.Errorf("appending sandbox ca to bundle: %w", err)
	}

	return nil
}

var systemBundleCandidates = []string{
	"/etc/ssl/certs/ca-certificates.crt", // Debian/Ubuntu/Alpine
	"/etc/pki/tls/certs/ca-bundle.crt",   // RHEL/CentOS/Fedora
	"/etc/ssl/cert.pem",                  // Alpine
	"/etc/ssl/ca-bundle.pem",             // SLES
}
