export { createSandbox, connectSandbox, DEFAULT_ENDPOINT } from "./sandbox.ts";
export { templates } from "./template.ts";
export { WsTransport } from "./transport.ts";
export type { RpcTransport } from "./transport.ts";
export type { Sandbox, SandboxOptions, ConnectOptions, ExecResult, SpawnHandle, PtyHandle } from "./sandbox.ts";
export type { TemplateManager } from "./template.ts";
export type { Auth, RequestSignerAuth, SignatureAuthOptions } from "./auth.ts";
export { signatureAuth } from "./auth.ts";
export {
  SandboxError,
  ConnectionError,
  RpcError,
  TimeoutError,
  FsError,
  CapacityError,
  SessionReplacedError,
} from "./errors.ts";
