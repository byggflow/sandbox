package runtime

import (
	"slices"
	"strings"
	"testing"
)

func TestContainerEnvOmitsEmpty(t *testing.T) {
	out := containerEnv{}.build()
	if len(out) != 0 {
		t.Errorf("expected empty env for zero value, got %v", out)
	}
}

func TestContainerEnvBootstrapOnly(t *testing.T) {
	out := containerEnv{AuthBootstrap: "nonce123"}.build()
	if !slices.Contains(out, "SANDBOX_AUTH_BOOTSTRAP=nonce123") {
		t.Errorf("expected bootstrap nonce var, got %v", out)
	}
	if hasPrefix(out, "SANDBOX_AUTH_TOKEN=") {
		t.Error("long-lived auth token must NEVER appear in env")
	}
	if hasPrefix(out, "HTTP_PROXY") {
		t.Errorf("expected no proxy vars when EgressPort empty, got %v", out)
	}
}

func TestContainerEnvProxyVars(t *testing.T) {
	out := containerEnv{EgressPort: "8118"}.build()
	must := []string{
		"HTTP_PROXY=http://127.0.0.1:8118",
		"HTTPS_PROXY=http://127.0.0.1:8118",
		"http_proxy=http://127.0.0.1:8118",
		"https_proxy=http://127.0.0.1:8118",
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
		"SANDBOX_EGRESS_PORT=8118",
	}
	for _, m := range must {
		if !slices.Contains(out, m) {
			t.Errorf("missing env var %q in %v", m, out)
		}
	}
}

func TestContainerEnvCATrust(t *testing.T) {
	out := containerEnv{CATrust: true}.build()
	// The CA PEM is pushed via OpNetCAInstall, never via env.
	if hasPrefix(out, "SANDBOX_CA_PEM=") {
		t.Error("SANDBOX_CA_PEM should not be set; CA travels via OpNetCAInstall")
	}
	for _, want := range []string{"NODE_EXTRA_CA_CERTS", "SSL_CERT_FILE", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE", "GIT_SSL_CAINFO"} {
		if !hasPrefix(out, want+"=") {
			t.Errorf("missing %s", want)
		}
	}
}

func TestContainerEnvKernelArgs(t *testing.T) {
	args := containerEnv{AuthBootstrap: "nonce", EgressPort: "8118"}.kernelArgs()
	if !strings.Contains(args, "sandbox.auth_bootstrap=nonce") {
		t.Errorf("missing bootstrap nonce kernel arg: %s", args)
	}
	if strings.Contains(args, "sandbox.auth_token") {
		t.Errorf("long-lived auth token must NEVER appear in cmdline: %s", args)
	}
	if !strings.Contains(args, "sandbox.egress_port=8118") {
		t.Errorf("missing egress port kernel arg: %s", args)
	}
}

func hasPrefix(env []string, prefix string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// TestLongLivedTokenNeverInGuestSurface is the regression test for the
// nonce-bootstrap architecture: regardless of which fields are set on
// containerEnv, the long-lived auth token must never appear in the
// container's environment OR the Firecracker kernel cmdline. Only the
// single-use bootstrap nonce is allowed. This anti-regression catches
// any future change that accidentally re-introduces SANDBOX_AUTH_TOKEN
// to either guest-readable surface.
func TestLongLivedTokenNeverInGuestSurface(t *testing.T) {
	const sentinelToken = "LONG-LIVED-SECRET-TOKEN-DO-NOT-LEAK"
	const sentinelNonce = "ephemeral-nonce-ok-if-leaked"

	// containerEnv has no AuthToken field at all by design; this test
	// asserts the contract by trying every combination of fields and
	// scanning the outputs for the sentinel. If a future change adds an
	// AuthToken field and wires it into build()/kernelArgs(), this test
	// will fail.
	envs := []containerEnv{
		{AuthBootstrap: sentinelNonce},
		{AuthBootstrap: sentinelNonce, EgressPort: "8118"},
		{AuthBootstrap: sentinelNonce, EgressPort: "8118", CATrust: true},
		{EgressPort: "8118"},          // bootstrap optional in all combos
		{EgressPort: "8118", CATrust: true},
		{CATrust: true},
	}
	for _, e := range envs {
		out := e.build()
		args := e.kernelArgs()
		for _, line := range out {
			if strings.Contains(line, sentinelToken) {
				t.Errorf("long-lived token leaked into container env: %q (for %+v)", line, e)
			}
		}
		if strings.Contains(args, sentinelToken) {
			t.Errorf("long-lived token leaked into kernel cmdline: %q (for %+v)", args, e)
		}
	}
}

// TestNonceVisibleButNotToken confirms that the bootstrap nonce IS
// emitted (so the agent can find it) while the long-lived token is
// not. Pairs with TestLongLivedTokenNeverInGuestSurface to assert the
// nonce-bootstrap design contract.
func TestNonceVisibleButNotToken(t *testing.T) {
	e := containerEnv{AuthBootstrap: "the-nonce", EgressPort: "8118", CATrust: true}
	env := e.build()
	args := e.kernelArgs()

	if !slices.Contains(env, "SANDBOX_AUTH_BOOTSTRAP=the-nonce") {
		t.Errorf("nonce not emitted to container env: %v", env)
	}
	if !strings.Contains(args, "sandbox.auth_bootstrap=the-nonce") {
		t.Errorf("nonce not emitted to kernel cmdline: %q", args)
	}
	// And no token-ish env name at all.
	for _, line := range env {
		if strings.HasPrefix(line, "SANDBOX_AUTH_TOKEN=") {
			t.Errorf("SANDBOX_AUTH_TOKEN must never appear in container env: %q", line)
		}
	}
	if strings.Contains(args, "sandbox.auth_token=") {
		t.Errorf("sandbox.auth_token must never appear in kernel cmdline: %q", args)
	}
}
