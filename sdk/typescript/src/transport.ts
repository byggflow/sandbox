import { ConnectionError, RpcError, SessionReplacedError } from "./errors.ts";

export interface RpcTransport {
  call(method: string, params: unknown): Promise<unknown>;
  notify(method: string, params: unknown): void;
  onNotification(handler: (method: string, params: unknown) => void): void;
  onReplaced(handler: () => void): void;
  close(): Promise<void>;
  sendBinary(data: Uint8Array): void;
  onBinary(handler: (data: Uint8Array) => void): void;
}

interface PendingRequest {
  resolve: (value: unknown) => void;
  reject: (reason: unknown) => void;
}

export class WsTransport implements RpcTransport {
  private ws: WebSocket | null = null;
  private nextId = 1;
  private pending = new Map<number, PendingRequest>();
  private notificationHandlers: Array<(method: string, params: unknown) => void> = [];
  private replacedHandlers: Array<() => void> = [];
  private binaryHandler: ((data: Uint8Array) => void) | null = null;
  private connectPromise: Promise<void> | null = null;

  async connect(url: string, headers?: Record<string, string>): Promise<void> {
    if (this.connectPromise) return this.connectPromise;

    this.connectPromise = new Promise<void>((resolve, reject) => {
      try {
        this.ws = new WebSocket(url, headers ? Object.entries(headers).map(([k, v]) => `${k}:${v}`) : undefined);
      } catch {
        // Bun's WebSocket accepts headers directly in some configurations.
        // Fall back to no subprotocol if the above fails.
        this.ws = new WebSocket(url);
      }

      this.ws.binaryType = "arraybuffer";

      this.ws.onopen = () => resolve();

      this.ws.onerror = (ev) => {
        const msg = ev instanceof ErrorEvent ? ev.message : "WebSocket error";
        reject(new ConnectionError(msg));
      };

      this.ws.onclose = (ev) => {
        // Reject all pending requests.
        for (const [, p] of this.pending) {
          p.reject(new ConnectionError(`WebSocket closed: ${ev.code} ${ev.reason}`));
        }
        this.pending.clear();
      };

      this.ws.onmessage = (ev) => {
        if (ev.data instanceof ArrayBuffer) {
          if (this.binaryHandler) {
            const handler = this.binaryHandler;
            this.binaryHandler = null;
            handler(new Uint8Array(ev.data));
          }
          return;
        }

        let msg: { id?: number; method?: string; params?: unknown; result?: unknown; error?: { code: number; message: string } };
        try {
          msg = JSON.parse(ev.data as string);
        } catch {
          return;
        }

        // JSON-RPC response (has id).
        if (msg.id !== undefined) {
          const p = this.pending.get(msg.id);
          if (!p) return;
          this.pending.delete(msg.id);

          if (msg.error) {
            p.reject(new RpcError(msg.error.message, msg.error.code));
          } else {
            p.resolve(msg.result);
          }
          return;
        }

        // JSON-RPC notification (no id).
        if (msg.method) {
          if (msg.method === "session.replaced") {
            for (const h of this.replacedHandlers) h();
            this.ws?.close();
            return;
          }

          for (const h of this.notificationHandlers) {
            h(msg.method, msg.params);
          }
        }
      };
    });

    return this.connectPromise;
  }

  call(method: string, params: unknown): Promise<unknown> {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      return Promise.reject(new ConnectionError("WebSocket not connected"));
    }

    const id = this.nextId++;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws!.send(JSON.stringify({ jsonrpc: "2.0", id, method, params }));
    });
  }

  notify(method: string, params: unknown): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      throw new ConnectionError("WebSocket not connected");
    }
    this.ws.send(JSON.stringify({ jsonrpc: "2.0", method, params }));
  }

  onNotification(handler: (method: string, params: unknown) => void): void {
    this.notificationHandlers.push(handler);
  }

  onReplaced(handler: () => void): void {
    this.replacedHandlers.push(handler);
  }

  sendBinary(data: Uint8Array): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      throw new ConnectionError("WebSocket not connected");
    }
    this.ws.send(data);
  }

  onBinary(handler: (data: Uint8Array) => void): void {
    this.binaryHandler = handler;
  }

  async close(): Promise<void> {
    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this.pending.clear();
    this.connectPromise = null;
  }
}
