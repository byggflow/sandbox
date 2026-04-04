export class SandboxError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "SandboxError";
  }
}

export class ConnectionError extends SandboxError {
  constructor(message: string) {
    super(message);
    this.name = "ConnectionError";
  }
}

export class RpcError extends SandboxError {
  code: number;
  constructor(message: string, code: number) {
    super(message);
    this.name = "RpcError";
    this.code = code;
  }
}

export class TimeoutError extends SandboxError {
  constructor(message: string) {
    super(message);
    this.name = "TimeoutError";
  }
}

export class FsError extends SandboxError {
  code: string;
  constructor(message: string, code: string) {
    super(message);
    this.name = "FsError";
    this.code = code;
  }
}

export class CapacityError extends SandboxError {
  retryAfter: number;
  constructor(message: string, retryAfter: number) {
    super(message);
    this.name = "CapacityError";
    this.retryAfter = retryAfter;
  }
}

export class SessionReplacedError extends ConnectionError {
  constructor() {
    super("Session replaced by a new connection");
    this.name = "SessionReplacedError";
  }
}
