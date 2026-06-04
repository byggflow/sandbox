package runtime

import "strings"

// containerEnv builds the environment passed to a sandbox container. It
// exists so the long list of KEY=VALUE strings sent to Docker/Firecracker
// is described in one place and shaped by what's actually configured, not
// by string-concat in the runtime's Create.
//
// The zero value is usable; only fields that should be set in the
// container appear in the output.
//
// Neither the long-lived auth token nor the CA cert is carried here —
// both are delivered to the agent over the first authenticated
// connection via auth.bootstrap and OpNetCAInstall respectively. The
// guest-readable surface (env / cmdline) only ever sees a single-use
// bootstrap nonce that's immediately consumed.
type containerEnv struct {
	// AuthBootstrap is the single-use nonce the agent trades for the
	// real token via auth.bootstrap. Exported as
	// SANDBOX_AUTH_BOOTSTRAP; the agent unsets it on read.
	AuthBootstrap string
	EgressPort    string // SANDBOX_EGRESS_PORT; also seeds HTTP_PROXY etc.
	// CATrust toggles the trust-bundle env vars (NODE_EXTRA_CA_CERTS, ...).
	// Set to true when the daemon will push a CA via OpNetCAInstall.
	CATrust bool
}

// build returns the env in KEY=VALUE form suitable for Docker's
// container.Config.Env. Keys are emitted in a deterministic order so
// container-creation diffs stay stable.
func (e containerEnv) build() []string {
	var out []string

	if e.AuthBootstrap != "" {
		out = append(out, "SANDBOX_AUTH_BOOTSTRAP="+e.AuthBootstrap)
	}

	if e.EgressPort != "" {
		proxyURL := "http://127.0.0.1:" + e.EgressPort
		out = append(out,
			"HTTP_PROXY="+proxyURL,
			"HTTPS_PROXY="+proxyURL,
			"http_proxy="+proxyURL,
			"https_proxy="+proxyURL,
			"NO_PROXY=127.0.0.1,localhost",
			"no_proxy=127.0.0.1,localhost",
			"SANDBOX_EGRESS_PORT="+e.EgressPort,
		)
	}

	if e.CATrust {
		out = append(out,
			"NODE_EXTRA_CA_CERTS=/tmp/sandbox-ca.crt",
			"SSL_CERT_FILE=/tmp/sandbox-ca-bundle.crt",
			"REQUESTS_CA_BUNDLE=/tmp/sandbox-ca-bundle.crt",
			"CURL_CA_BUNDLE=/tmp/sandbox-ca-bundle.crt",
			"GIT_SSL_CAINFO=/tmp/sandbox-ca-bundle.crt",
		)
	}

	return out
}

// kernelArgs returns the egress-related hints as Firecracker kernel
// command-line tokens (sandbox.foo=bar). Used by the in-VM init script
// to export the matching env vars.
//
// Only the single-use bootstrap nonce ever appears here — never the
// long-lived auth token. /proc/cmdline is world-readable inside the
// guest, but the nonce is invalidated the moment the daemon trades it
// for the real token, so a sandbox process that reads it gains
// nothing. CA PEM is pushed post-boot via OpNetCAInstall and never
// touches cmdline either way.
func (e containerEnv) kernelArgs() string {
	var parts []string
	if e.EgressPort != "" {
		parts = append(parts,
			"sandbox.egress_port="+e.EgressPort,
			"sandbox.http_proxy=http://127.0.0.1:"+e.EgressPort,
		)
	}
	if e.AuthBootstrap != "" {
		parts = append(parts, "sandbox.auth_bootstrap="+e.AuthBootstrap)
	}
	return strings.Join(parts, " ")
}
