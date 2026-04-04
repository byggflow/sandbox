import type { RpcTransport } from "./transport.ts";

export interface CallContext {
  transport: RpcTransport;
  sandboxId: string;
}

export async function call(ctx: CallContext, op: { method: string; params?: unknown }): Promise<unknown> {
  return ctx.transport.call(op.method, op.params);
}
