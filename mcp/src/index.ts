#!/usr/bin/env bun
import { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { StdioServerTransport } from "@modelcontextprotocol/sdk/server/stdio.js";
import { registerTools } from "./tools.ts";

const server = new McpServer({
  name: "@byggflow/sandbox-mcp",
  version: "0.0.1",
});

registerTools(server);

const transport = new StdioServerTransport();
await server.connect(transport);
