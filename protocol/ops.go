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

	// Template
	OpTemplateSave = "template.save"

	// Session
	OpSessionResumed    = "session.resumed"
	OpSessionReplaced   = "session.replaced"
	OpSessionNegotiateE2E = "session.negotiate_e2e"
)
