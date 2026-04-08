/** Base error for all sandbox operations. */
export class SandboxError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "SandboxError";
  }
}

/** Thrown when a WebSocket or HTTP connection to the daemon fails. */
export class ConnectionError extends SandboxError {
  constructor(message: string) {
    super(message);
    this.name = "ConnectionError";
  }
}

/** Thrown when an RPC call returns an error response. */
export class RpcError extends SandboxError {
  code: number;
  constructor(message: string, code: number) {
    super(message);
    this.name = "RpcError";
    this.code = code;
  }
}

/** Thrown when an operation exceeds its deadline. */
export class TimeoutError extends SandboxError {
  constructor(message: string) {
    super(message);
    this.name = "TimeoutError";
  }
}

/** Thrown when a filesystem operation fails inside the sandbox. */
export class FsError extends SandboxError {
  code: string;
  constructor(message: string, code: string) {
    super(message);
    this.name = "FsError";
    this.code = code;
  }
}

/** Thrown when the daemon cannot allocate a sandbox due to capacity limits. */
export class CapacityError extends SandboxError {
  retryAfter: number;
  constructor(message: string, retryAfter: number) {
    super(message);
    this.name = "CapacityError";
    this.retryAfter = retryAfter;
  }
}

/** Thrown when another client takes over the sandbox session. */
export class SessionReplacedError extends ConnectionError {
  constructor() {
    super("Session replaced by a new connection");
    this.name = "SessionReplacedError";
  }
}
