package protocol

// Operation names for the sandbox API.
const (
	// Filesystem
	OpFsRead     = "fs.read"
	OpFsWrite    = "fs.write"
	OpFsList     = "fs.list"
	OpFsStat     = "fs.stat"
	OpFsRemove   = "fs.remove"
	OpFsRename   = "fs.rename"
	OpFsMkdir    = "fs.mkdir"
	OpFsUpload   = "fs.upload"
	OpFsDownload = "fs.download"

	// Process
	OpProcessExec   = "process.exec"
	OpProcessStream = "process.stream"
	OpProcessSpawn  = "process.spawn"
	OpProcessPty    = "process.pty"
	OpProcessResize = "process.resize"

	// Streaming process output notifications
	OpProcessOutput = "process.output"
	OpProcessDone   = "process.done"

	// Environment
	OpEnvGet    = "env.get"
	OpEnvSet    = "env.set"
	OpEnvDelete = "env.delete"
	OpEnvList   = "env.list"

	// Network
	OpNetFetch = "net.fetch"

	// Network egress middleware. Agent-initiated: the agent's in-sandbox
	// proxy receives an HTTP request from sandbox code, then invokes
	// OpNetEgress on the daemon. The daemon applies registered rules
	// (inject headers, allow, deny, mutate) and returns the upstream
	// response. This is how SDK-side credential injection works without
	// the credentials ever entering the sandbox.
	OpNetEgress = "net.egress"

	// OpNetEgressStream is the streaming variant of OpNetEgress. The
	// agent supplies a stream ID in the params. The daemon's response
	// returns the upstream status + headers as soon as they arrive,
	// and pushes the body in FrameStreamData frames addressed to that
	// stream ID, terminating with a FrameStreamEnd frame. Used for
	// large responses and SSE / chunked transfer (e.g. LLM streaming).
	OpNetEgressStream = "net.egress.stream"

	// Daemon -> SDK notification: a registered rule push request.
	// Sent from the SDK to install/update rules for a given sandbox.
	OpNetRulesSet = "net.rules.set"

	// Agent -> Daemon: request a leaf certificate for an SNI host. The
	// daemon signs with the per-sandbox CA and returns cert+key PEMs. Used
	// by the agent's HTTPS CONNECT handler to MITM the sandbox's TLS.
	OpNetCertLeaf = "net.cert.leaf"

	// Daemon -> Agent: install the per-sandbox CA certificate. Called by
	// the runtime once during the readiness check, before the sandbox is
	// reported as ready. The agent writes /tmp/sandbox-ca.crt and a merged
	// trust bundle so user processes can validate leaf certs minted by
	// the daemon for HTTPS interception.
	OpNetCAInstall = "net.ca.install"

	// Daemon -> SDK: a deferred-handler invocation. When a matched egress
	// rule was registered with a programmatic handler (rather than a
	// declarative inject/allow/deny), the daemon pauses the request and
	// sends this RPC up the WebSocket. The SDK runs the user's handler
	// and returns a DeferResponse with either a modified request (to
	// continue dialing) or a synthetic response (to short-circuit).
	OpNetDefer = "net.defer"

	// Template
	OpTemplateSave = "template.save"

	// Session
	OpSessionResumed    = "session.resumed"
	OpSessionReplaced   = "session.replaced"
	OpSessionNegotiateE2E = "session.negotiate_e2e"

	// Auth bootstrap: the daemon's very first connection presents a
	// single-use nonce (delivered to the guest via cmdline/env at boot)
	// and, on success, receives the long-lived auth token back. The
	// nonce is invalidated immediately. All subsequent connections use
	// the standard auth.token RPC.
	//
	// Goal: keep the long-lived token out of any guest-readable surface
	// (kernel cmdline on Firecracker, inherited env on Docker). The
	// nonce is one-shot — even if leaked, it can't be replayed.
	OpAuthBootstrap = "auth.bootstrap"
)
