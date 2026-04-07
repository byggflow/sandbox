import { test, expect, describe, vi, beforeEach } from "vitest";
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { Client } from "@modelcontextprotocol/sdk/client/index.js";
import { StdioClientTransport } from "@modelcontextprotocol/sdk/client/stdio.js";

// ── Integration tests: spawn the real MCP server process ─────────

function createClient(): { client: Client; transport: StdioClientTransport } {
  const transport = new StdioClientTransport({
    command: "bun",
    args: [new URL("./index.ts", import.meta.url).pathname],
    stderr: "pipe",
  });
  const client = new Client({ name: "test-client", version: "1.0.0" });
  return { client, transport };
}

describe("MCP server protocol", () => {
  let client: Client;
  let transport: StdioClientTransport;

  beforeEach(async () => {
    const c = createClient();
    client = c.client;
    transport = c.transport;
    await client.connect(transport);

    return async () => {
      await transport.close();
    };
  });

  test("lists all tools", async () => {
    // Retry with a fresh connection if the server wasn't ready.
    let result;
    for (let attempt = 0; attempt < 3; attempt++) {
      try {
        result = await client.listTools();
        break;
      } catch {
        if (attempt === 2) throw new Error("server not ready after 3 attempts");
        await transport.close();
        const c = createClient();
        client = c.client;
        transport = c.transport;
        await client.connect(transport);
      }
    }
    const names = result!.tools.map((t) => t.name).sort();
    expect(names).toEqual([
      "sandbox_close_port",
      "sandbox_create",
      "sandbox_create_template",
      "sandbox_destroy",
      "sandbox_download",
      "sandbox_edit_file",
      "sandbox_eval",
      "sandbox_exec",
      "sandbox_expose_port",
      "sandbox_list",
      "sandbox_list_files",
      "sandbox_list_ports",
      "sandbox_list_profiles",
      "sandbox_list_templates",
      "sandbox_port_url",
      "sandbox_read_file",
      "sandbox_upload",
      "sandbox_write_file",
    ]);
  });

  test("each tool has a description", async () => {
    const result = await client.listTools();
    for (const tool of result.tools) {
      expect(tool.description).toBeTruthy();
      expect(tool.description!.length).toBeGreaterThan(10);
    }
  });

  test("each tool has an input schema", async () => {
    const result = await client.listTools();
    for (const tool of result.tools) {
      expect(tool.inputSchema).toBeDefined();
      expect(tool.inputSchema.type).toBe("object");
    }
  });

  test("sandbox_create requires no parameters", async () => {
    const result = await client.listTools();
    const create = result.tools.find((t) => t.name === "sandbox_create")!;
    // All parameters are optional
    expect(create.inputSchema.required ?? []).toEqual([]);
  });

  test("sandbox_exec requires sandbox_id and command", async () => {
    const result = await client.listTools();
    const exec = result.tools.find((t) => t.name === "sandbox_exec")!;
    const required = (exec.inputSchema.required ?? []) as string[];
    expect(required).toContain("sandbox_id");
    expect(required).toContain("command");
  });

  test("sandbox_eval requires sandbox_id, code, path, and command", async () => {
    const result = await client.listTools();
    const evalTool = result.tools.find((t) => t.name === "sandbox_eval")!;
    const required = (evalTool.inputSchema.required ?? []) as string[];
    expect(required).toContain("sandbox_id");
    expect(required).toContain("code");
    expect(required).toContain("path");
    expect(required).toContain("command");
  });

  test("sandbox_edit_file requires sandbox_id, path, and mode", async () => {
    const result = await client.listTools();
    const edit = result.tools.find((t) => t.name === "sandbox_edit_file")!;
    const required = (edit.inputSchema.required ?? []) as string[];
    expect(required).toContain("sandbox_id");
    expect(required).toContain("path");
    expect(required).toContain("mode");
  });

  test("sandbox_eval has timeout parameter", async () => {
    const result = await client.listTools();
    const evalTool = result.tools.find((t) => t.name === "sandbox_eval")!;
    const props = evalTool.inputSchema.properties as Record<string, any>;
    expect(props.timeout).toBeDefined();
  });
});

// ── Error handling tests: tools fail gracefully without a daemon ──

describe("MCP tools error handling (no daemon)", () => {
  let client: Client;
  let transport: StdioClientTransport;

  beforeEach(async () => {
    const c = createClient();
    client = c.client;
    transport = c.transport;
    await client.connect(transport);

    return async () => {
      await transport.close();
    };
  });

  test("sandbox_list returns error when no daemon available", async () => {
    const result = await client.callTool({ name: "sandbox_list", arguments: {} });
    // Should return an error response, not crash
    expect(result.isError).toBe(true);
    const text = (result.content as Array<{ type: string; text: string }>)[0].text;
    const parsed = JSON.parse(text);
    expect(parsed.error).toBeDefined();
  });

  test("sandbox_exec returns error for nonexistent sandbox", async () => {
    const result = await client.callTool({
      name: "sandbox_exec",
      arguments: { sandbox_id: "nonexistent", command: "echo hello" },
    });
    expect(result.isError).toBe(true);
    const text = (result.content as Array<{ type: string; text: string }>)[0].text;
    const parsed = JSON.parse(text);
    expect(parsed.error).toBeDefined();
  });

  test("sandbox_destroy returns error for nonexistent sandbox", async () => {
    const result = await client.callTool({
      name: "sandbox_destroy",
      arguments: { sandbox_id: "nonexistent" },
    });
    expect(result.isError).toBe(true);
  });

  test("sandbox_read_file returns error for nonexistent sandbox", async () => {
    const result = await client.callTool({
      name: "sandbox_read_file",
      arguments: { sandbox_id: "nonexistent", path: "/etc/hosts" },
    });
    expect(result.isError).toBe(true);
  });

  test("sandbox_upload returns error for nonexistent local file", async () => {
    const result = await client.callTool({
      name: "sandbox_upload",
      arguments: {
        sandbox_id: "nonexistent",
        source: "/tmp/does-not-exist-1234567890.txt",
        destination: "/tmp/test.txt",
      },
    });
    expect(result.isError).toBe(true);
  });
});
