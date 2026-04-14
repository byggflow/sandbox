/**
 * Unix domain socket transport for HTTP and WebSocket.
 *
 * Uses node:http and node:crypto (available in Node, Bun, and Deno) to
 * communicate over Unix sockets without requiring TCP. This module is
 * dynamically imported only when a unix:// endpoint is used, so it does
 * not break browser builds.
 */

import { request as httpRequest } from "node:http";
import { randomBytes } from "node:crypto";
import type { IncomingMessage } from "node:http";
import type { Socket } from "node:net";

// ── HTTP over Unix socket ───────────────────────────────────────

/** Perform an HTTP request over a Unix domain socket. Returns a standard Response. */
export function unixFetch(
  socketPath: string,
  path: string,
  init?: { method?: string; headers?: Record<string, string>; body?: string },
): Promise<Response> {
  return new Promise((resolve, reject) => {
    const req = httpRequest(
      {
        socketPath,
        path,
        method: init?.method ?? "GET",
        headers: init?.headers,
      },
      (res: IncomingMessage) => {
        const chunks: Buffer[] = [];
        res.on("data", (chunk: Buffer) => chunks.push(chunk));
        res.on("end", () => {
          const body = Buffer.concat(chunks).toString("utf-8");
          const status = res.statusCode ?? 0;
          const headers: Record<string, string> = {};
          for (const [key, value] of Object.entries(res.headers)) {
            if (value) headers[key] = Array.isArray(value) ? value.join(", ") : value;
          }
          resolve(new Response(body, { status, headers }));
        });
        res.on("error", reject);
      },
    );
    req.on("error", reject);
    if (init?.body) req.write(init.body);
    req.end();
  });
}

// ── WebSocket over Unix socket ──────────────────────────────────

/** Opcodes defined by RFC 6455. */
const enum WsOpcode {
  Text = 0x1,
  Binary = 0x2,
  Close = 0x8,
  Ping = 0x9,
  Pong = 0xa,
}

/**
 * Minimal WebSocket implementation over a Unix domain socket.
 *
 * Speaks enough of RFC 6455 to support the SDK's RPC transport:
 * text frames, binary frames, close, and ping/pong.
 */
export class UnixWebSocket {
  private socket: Socket | null = null;
  private buf = Buffer.alloc(0);
  private closed = false;

  binaryType: "arraybuffer" | "blob" = "arraybuffer";
  onopen: (() => void) | null = null;
  onclose: ((ev: { code: number; reason: string }) => void) | null = null;
  onerror: ((ev: { message: string }) => void) | null = null;
  onmessage: ((ev: { data: string | ArrayBuffer }) => void) | null = null;

  get readyState(): number {
    if (!this.socket || this.closed) return 3; // CLOSED
    return 1; // OPEN
  }

  /** Connect via Unix socket, performing the HTTP upgrade handshake. */
  connect(socketPath: string, path: string, headers?: Record<string, string>): Promise<void> {
    return new Promise((resolve, reject) => {
      const key = randomBytes(16).toString("base64");

      const reqHeaders: Record<string, string> = {
        Host: "localhost",
        Connection: "Upgrade",
        Upgrade: "websocket",
        "Sec-WebSocket-Version": "13",
        "Sec-WebSocket-Key": key,
        ...headers,
      };

      const req = httpRequest({ socketPath, path, method: "GET", headers: reqHeaders });

      req.on("upgrade", (_res: IncomingMessage, socket: Socket, head: Buffer) => {
        this.socket = socket;
        if (head.length > 0) this.buf = Buffer.from(head);

        socket.on("data", (data: Buffer) => {
          this.buf = Buffer.concat([this.buf, data]);
          this.drain();
        });

        socket.on("close", () => {
          if (this.closed) return;
          this.closed = true;
          this.onclose?.({ code: 1006, reason: "" });
        });

        socket.on("error", (err) => {
          this.onerror?.({ message: err.message });
        });

        this.onopen?.();
        resolve();
      });

      req.on("error", (err) => {
        reject(err);
        this.onerror?.({ message: err.message });
      });

      req.on("response", (res: IncomingMessage) => {
        reject(new Error(`WebSocket upgrade failed: HTTP ${res.statusCode}`));
      });

      req.end();
    });
  }

  send(data: string | ArrayBuffer | Uint8Array): void {
    if (!this.socket || this.closed) throw new Error("WebSocket not connected");

    const isText = typeof data === "string";
    const payload = isText
      ? Buffer.from(data, "utf-8")
      : Buffer.from(
          data instanceof ArrayBuffer ? data : data.buffer,
          data instanceof ArrayBuffer ? 0 : data.byteOffset,
          data instanceof ArrayBuffer ? data.byteLength : data.byteLength,
        );

    this.socket.write(encodeFrame(isText ? WsOpcode.Text : WsOpcode.Binary, payload));
  }

  close(code = 1000, reason = ""): void {
    if (!this.socket || this.closed) return;
    this.closed = true;

    const reasonBuf = Buffer.from(reason, "utf-8");
    const payload = Buffer.alloc(2 + reasonBuf.length);
    payload.writeUInt16BE(code, 0);
    reasonBuf.copy(payload, 2);
    try {
      this.socket.write(encodeFrame(WsOpcode.Close, payload));
    } catch {
      // Socket may already be dead.
    }
    this.socket.end();
  }

  private drain(): void {
    while (this.buf.length >= 2) {
      const result = decodeFrame(this.buf);
      if (!result) break;

      const { opcode, payload, totalLen } = result;
      this.buf = this.buf.subarray(totalLen);

      switch (opcode) {
        case WsOpcode.Text:
          this.onmessage?.({ data: payload.toString("utf-8") });
          break;
        case WsOpcode.Binary:
          this.onmessage?.({
            data: payload.buffer.slice(payload.byteOffset, payload.byteOffset + payload.byteLength),
          });
          break;
        case WsOpcode.Close: {
          const code = payload.length >= 2 ? payload.readUInt16BE(0) : 1005;
          const reason = payload.length > 2 ? payload.subarray(2).toString("utf-8") : "";
          this.closed = true;
          this.onclose?.({ code, reason });
          this.socket?.end();
          break;
        }
        case WsOpcode.Ping:
          if (this.socket && !this.closed) {
            this.socket.write(encodeFrame(WsOpcode.Pong, payload));
          }
          break;
        case WsOpcode.Pong:
          break;
      }
    }
  }
}

// ── WebSocket framing (RFC 6455) ────────────────────────────────

/** Encode a WebSocket frame. Client frames are always masked per spec. */
function encodeFrame(opcode: number, payload: Buffer): Buffer {
  const len = payload.length;
  let headerLen = 2 + 4; // base header + mask key
  if (len > 125 && len <= 0xffff) headerLen += 2;
  else if (len > 0xffff) headerLen += 8;

  const frame = Buffer.alloc(headerLen + len);
  let offset = 0;

  frame[offset++] = 0x80 | opcode; // FIN + opcode

  if (len <= 125) {
    frame[offset++] = 0x80 | len; // mask bit + length
  } else if (len <= 0xffff) {
    frame[offset++] = 0x80 | 126;
    frame.writeUInt16BE(len, offset);
    offset += 2;
  } else {
    frame[offset++] = 0x80 | 127;
    frame.writeUInt32BE(Math.floor(len / 0x100000000), offset);
    frame.writeUInt32BE(len % 0x100000000, offset + 4);
    offset += 8;
  }

  const maskKey = randomBytes(4);
  maskKey.copy(frame, offset);
  offset += 4;
  for (let i = 0; i < len; i++) {
    frame[offset + i] = payload[i]! ^ maskKey[i % 4]!;
  }

  return frame;
}

/** Decode a single server frame (unmasked). Returns null if buffer is incomplete. */
function decodeFrame(buf: Buffer): { opcode: number; payload: Buffer; totalLen: number } | null {
  if (buf.length < 2) return null;

  const opcode = buf[0]! & 0x0f;
  const masked = (buf[1]! & 0x80) !== 0;
  let payloadLen = buf[1]! & 0x7f;
  let offset = 2;

  if (payloadLen === 126) {
    if (buf.length < 4) return null;
    payloadLen = buf.readUInt16BE(2);
    offset = 4;
  } else if (payloadLen === 127) {
    if (buf.length < 10) return null;
    payloadLen = buf.readUInt32BE(2) * 0x100000000 + buf.readUInt32BE(6);
    offset = 10;
  }

  if (masked) {
    if (buf.length < offset + 4 + payloadLen) return null;
    const maskKey = buf.subarray(offset, offset + 4);
    offset += 4;
    const payload = Buffer.alloc(payloadLen);
    for (let i = 0; i < payloadLen; i++) {
      payload[i] = buf[offset + i]! ^ maskKey[i % 4]!;
    }
    return { opcode, payload, totalLen: offset + payloadLen };
  }

  if (buf.length < offset + payloadLen) return null;
  return { opcode, payload: Buffer.from(buf.subarray(offset, offset + payloadLen)), totalLen: offset + payloadLen };
}
