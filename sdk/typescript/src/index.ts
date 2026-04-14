/**
 * @module
 *
 * TypeScript SDK for Byggflow sandboxes. Create isolated sandbox environments
 * with filesystem, process execution, environment variable, and network access.
 *
 * @example
 * ```ts
 * import { createSandbox } from "@byggflow/sandbox";
 *
 * const sandbox = await createSandbox({ endpoint: "https://api.byggflow.com" });
 *
 * // Execute a command
 * const result = await sandbox.process.exec("echo hello");
 * console.log(result.stdout); // "hello\n"
 *
 * // Read and write files
 * await sandbox.fs.write("/tmp/greeting.txt", "Hello, world!");
 * const content = await sandbox.fs.read("/tmp/greeting.txt");
 *
 * await sandbox.close();
 * ```
 */

export { createSandbox, connectSandbox, DEFAULT_ENDPOINT, resolveEndpoints } from "./sandbox.ts";
export type { ResolvedEndpoint } from "./sandbox.ts";
export { templates } from "./template.ts";
export { WsTransport } from "./transport.ts";
export type { RpcTransport } from "./transport.ts";
export type { Sandbox, SandboxOptions, ConnectOptions, ExecResult, SpawnHandle, PtyHandle, OutputEvent, StreamExecHandle, TunnelInfo } from "./sandbox.ts";
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
