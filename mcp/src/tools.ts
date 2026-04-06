import type { McpServer } from "@modelcontextprotocol/sdk/server/mcp.js";
import { z } from "zod";
import {
  createTrackedSandbox,
  createTemplate,
  destroySandbox,
  getSandbox,
  listProfiles,
  listSandboxes,
  listTemplates,
  McpError,
} from "./connection.ts";


function formatExecResult(result: { stdout: string; stderr: string; exitCode: number }): string {
  let out = "";
  if (result.stdout) out += result.stdout;
  if (result.stderr) out += (out ? "\n" : "") + `[stderr] ${result.stderr}`;
  out += (out ? "\n" : "") + `[exit_code: ${result.exitCode}]`;
  return out;
}

function errorResult(e: unknown): { content: Array<{ type: "text"; text: string }>; isError: true } {
  if (e instanceof McpError) {
    return { content: [{ type: "text", text: JSON.stringify(e.toJSON(), null, 2) }], isError: true };
  }
  const msg = e instanceof Error ? e.message : String(e);
  return { content: [{ type: "text", text: JSON.stringify({ error: "internal", message: msg }, null, 2) }], isError: true };
}

export function registerTools(server: McpServer): void {
  // ── Lifecycle ──────────────────────────────────────────────────

  server.tool(
    "sandbox_create",
    "Create a new isolated sandbox. Runs as root. Use sandbox_list_profiles to see available environments. Writable: /root, /tmp. Rest is read-only.",
    {
      profile: z.string().optional().describe("Profile name (e.g. \"python\", \"node\") — defines base image and resource defaults"),
      template: z.string().optional().describe("Template ID to use a snapshot of a previously saved sandbox"),
      memory: z.string().optional().describe('Memory limit (e.g. "512m", "1g")'),
      cpu: z.number().optional().describe("vCPU count"),
      ttl: z.number().optional().describe("Seconds to keep alive after disconnect"),
    },
    async ({ profile, template, memory, cpu, ttl }) => {
      try {
        const sandbox = await createTrackedSandbox({ profile, template, memory, cpu, ttl });
        return {
          content: [{
            type: "text",
            text: JSON.stringify({
              sandbox_id: sandbox.id,
              status: "running",
              writable_paths: ["/root", "/tmp"],
              user: "root",
            }),
          }],
        };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  server.tool(
    "sandbox_destroy",
    "Destroy a sandbox and clean up all resources",
    {
      sandbox_id: z.string().describe("Sandbox ID"),
    },
    async ({ sandbox_id }) => {
      try {
        await destroySandbox(sandbox_id);
        return { content: [{ type: "text", text: JSON.stringify({ success: true }) }] };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  server.tool(
    "sandbox_list",
    "List active sandboxes for the current identity",
    {
      status: z
        .enum(["running", "suspended", "all"])
        .optional()
        .describe('Filter by status (default: "running")'),
    },
    async ({ status }) => {
      try {
        const result = await listSandboxes(status ?? "running");
        return { content: [{ type: "text", text: JSON.stringify({ sandboxes: result }, null, 2) }] };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  server.tool(
    "sandbox_list_templates",
    "List saved sandbox snapshots (templates) that can restore a sandbox to a previous state",
    {},
    async () => {
      try {
        const result = await listTemplates();
        return { content: [{ type: "text", text: JSON.stringify({ templates: result }, null, 2) }] };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  server.tool(
    "sandbox_list_profiles",
    "List available profiles (base environments with pre-installed runtimes like python, node, go)",
    {},
    async () => {
      try {
        const result = await listProfiles();
        return { content: [{ type: "text", text: JSON.stringify({ profiles: result }, null, 2) }] };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  server.tool(
    "sandbox_create_template",
    "Snapshot a running sandbox into a reusable template. Use sandbox_list_templates to see saved templates.",
    {
      sandbox_id: z.string().describe("ID of the running sandbox to snapshot"),
      label: z.string().optional().describe("Human-readable label for this template"),
    },
    async ({ sandbox_id, label }) => {
      try {
        const result = await createTemplate(sandbox_id, label);
        return { content: [{ type: "text", text: JSON.stringify(result, null, 2) }] };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  // ── Execution ──────────────────────────────────────────────────

  server.tool(
    "sandbox_exec",
    "Run a shell command in a sandbox. For long-running commands, increase timeout.",
    {
      sandbox_id: z.string().describe("Sandbox ID"),
      command: z.string().describe("Shell command to execute"),
      timeout: z.number().optional().describe("Timeout in seconds (default: 30, max: 300)"),
      working_dir: z.string().optional().describe("Working directory for command"),
    },
    async ({ sandbox_id, command, timeout, working_dir }) => {
      try {
        const sandbox = await getSandbox(sandbox_id);
        // Shell-escape working_dir to prevent injection via crafted paths.
        const cmd = working_dir ? `cd -- '${working_dir.replace(/'/g, "'\\''")}' && ${command}` : command;
        const t = Math.min(timeout ?? 30, 300);
        const result = await sandbox.process.exec(cmd, { timeout: t });
        return {
          content: [{ type: "text", text: formatExecResult(result) }],
        };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  server.tool(
    "sandbox_eval",
    "Write code to a file and run a command. Write your code, specify where to save it, and what command to execute.",
    {
      sandbox_id: z.string().describe("Sandbox ID"),
      code: z.string().describe("Code to write"),
      path: z.string().describe("File path to write code to (e.g. /tmp/main.py)"),
      command: z.string().describe("Command to run (e.g. python3 /tmp/main.py)"),
      timeout: z.number().optional().describe("Timeout in seconds (default: 30, max: 300)"),
    },
    async ({ sandbox_id, code, path, command, timeout }) => {
      try {
        const sandbox = await getSandbox(sandbox_id);

        // Ensure parent directory exists
        const dir = path.substring(0, path.lastIndexOf("/"));
        if (dir) {
          await sandbox.process.exec(`mkdir -p ${JSON.stringify(dir)}`);
        }

        // Write the code file
        await sandbox.fs.write(path, code);

        // Execute
        const t = Math.min(timeout ?? 30, 300);
        const result = await sandbox.process.exec(command, { timeout: t });
        return {
          content: [{ type: "text", text: formatExecResult(result) }],
        };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  // ── File Operations ────────────────────────────────────────────

  server.tool(
    "sandbox_read_file",
    "Read file contents from a sandbox, with optional line range",
    {
      sandbox_id: z.string().describe("Sandbox ID"),
      path: z.string().describe("File path inside sandbox"),
      line_range: z
        .tuple([z.number(), z.number()])
        .optional()
        .describe("Start and end line (1-indexed, -1 = EOF)"),
    },
    async ({ sandbox_id, path, line_range }) => {
      try {
        const sandbox = await getSandbox(sandbox_id);
        const data = await sandbox.fs.read(path);
        let content = new TextDecoder().decode(data);

        if (line_range) {
          const allLines = content.split("\n");
          const start = Math.max(0, line_range[0] - 1);
          const end = line_range[1] === -1 ? allLines.length : line_range[1];
          content = allLines.slice(start, end).join("\n");
        }

        // Number each line for readability
        const numbered = content
          .split("\n")
          .map((l, i) => `${i + 1}: ${l}`)
          .join("\n");

        return {
          content: [{ type: "text", text: `${path} (${content.split("\n").length} lines)\n\n${numbered}` }],
        };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  server.tool(
    "sandbox_write_file",
    "Create or overwrite a file in a sandbox. Writable paths: /root and /tmp.",
    {
      sandbox_id: z.string().describe("Sandbox ID"),
      path: z.string().describe("File path inside sandbox (use /root/ or /tmp/)"),
      content: z.string().describe("File content"),
      mode: z
        .enum(["create", "append"])
        .optional()
        .describe('"create" (default, overwrites) or "append" (appends to existing file, ensures newline separator)'),
    },
    async ({ sandbox_id, path, content, mode }) => {
      try {
        const sandbox = await getSandbox(sandbox_id);

        if (mode === "append") {
          let existing = "";
          try {
            const data = await sandbox.fs.read(path);
            existing = new TextDecoder().decode(data);
          } catch {
            // File doesn't exist yet, that's fine
          }
          const separator = existing.length > 0 && !existing.endsWith("\n") ? "\n" : "";
          const combined = existing + separator + content;
          await sandbox.fs.write(path, combined);
          const totalLines = combined.split("\n").length;
          const appendedLines = content.split("\n").length;
          return {
            content: [{ type: "text", text: JSON.stringify({ path, bytes_written: new TextEncoder().encode(combined).byteLength, total_lines: totalLines, appended_lines: appendedLines }) }],
          };
        }

        await sandbox.fs.write(path, content);
        return {
          content: [{ type: "text", text: JSON.stringify({ path, bytes_written: new TextEncoder().encode(content).byteLength }) }],
        };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  server.tool(
    "sandbox_edit_file",
    "Make precise edits to an existing file using str_replace or insert",
    {
      sandbox_id: z.string().describe("Sandbox ID"),
      path: z.string().describe("File path inside sandbox"),
      mode: z.enum(["str_replace", "insert"]).describe('"str_replace" or "insert"'),
      old_str: z.string().optional().describe("Exact text to find (str_replace mode)"),
      new_str: z.string().optional().describe("Replacement text or text to insert"),
      line: z.number().optional().describe("Line number to insert before (insert mode, 1-indexed)"),
    },
    async ({ sandbox_id, path, mode, old_str, new_str, line }) => {
      try {
        const sandbox = await getSandbox(sandbox_id);
        const data = await sandbox.fs.read(path);
        let content = new TextDecoder().decode(data);

        if (mode === "str_replace") {
          if (old_str === undefined || new_str === undefined) {
            return errorResult(new McpError("edit_no_match", "str_replace mode requires old_str and new_str", "Provide both old_str and new_str parameters."));
          }

          const occurrences = content.split(old_str).length - 1;
          if (occurrences === 0) {
            return errorResult(new McpError("edit_no_match", `old_str not found in ${path}`, "Re-read the file and check the exact text."));
          }
          if (occurrences > 1) {
            return errorResult(new McpError("edit_multiple_matches", `old_str found ${occurrences} times in ${path}`, "Provide more context to make the match unique."));
          }

          content = content.replace(old_str, new_str);
        } else {
          // insert mode
          if (line === undefined || new_str === undefined) {
            return errorResult(new McpError("edit_no_match", "insert mode requires line and new_str", "Provide both line and new_str parameters."));
          }

          const lines = content.split("\n");
          const idx = Math.max(0, Math.min(line - 1, lines.length));
          lines.splice(idx, 0, new_str);
          content = lines.join("\n");
        }

        await sandbox.fs.write(path, content);

        // Build preview: show context around the edit
        const allLines = content.split("\n");
        const editLine = mode === "str_replace"
          ? allLines.findIndex((l) => l.includes(new_str?.split("\n")[0] ?? ""))
          : (line ?? 1) - 1;
        const previewStart = Math.max(0, editLine - 3);
        const previewEnd = Math.min(allLines.length, editLine + 4);
        const preview = allLines
          .slice(previewStart, previewEnd)
          .map((l, i) => `${previewStart + i + 1}: ${l}`)
          .join("\n");

        return {
          content: [{ type: "text", text: `${path} edited:\n\n${preview}` }],
        };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  server.tool(
    "sandbox_list_files",
    "List files and directories in a sandbox. Excludes virtual filesystems (/proc, /sys, /dev).",
    {
      sandbox_id: z.string().describe("Sandbox ID"),
      path: z.string().optional().describe('Directory path (default: "/root")'),
      depth: z.number().optional().describe("Recursion depth (max 3, default: 1)"),
    },
    async ({ sandbox_id, path, depth }) => {
      try {
        const sandbox = await getSandbox(sandbox_id);
        const dir = path ?? "/root";
        const maxDepth = Math.min(depth ?? 1, 3);

        const escDir = dir.replace(/'/g, "'\\''");
        const excludes = "-not -path '*/proc/*' -not -path '*/sys/*' -not -path '*/dev/*'";
        const cmd = `find '${escDir}' -maxdepth ${maxDepth} -not -path '${escDir}' ${excludes} 2>/dev/null | while IFS= read -r f; do if [ -d "$f" ]; then printf 'd\\t0\\t%s\\n' "$f"; else s=$(stat -c %s "$f" 2>/dev/null || stat -f %z "$f" 2>/dev/null || echo 0); printf 'f\\t%s\\t%s\\n' "$s" "$f"; fi; done`;

        const result = await sandbox.process.exec(cmd, { timeout: 10 });
        const entries = result.stdout
          .trim()
          .split("\n")
          .filter(Boolean)
          .map((line) => {
            const [typeChar, sizeStr, ...nameParts] = line.split("\t");
            const type = typeChar === "d" ? "directory" : "file";
            const size = parseInt(sizeStr ?? "0", 10);
            const name = nameParts.join("\t");
            return { name, type, size };
          });

        return {
          content: [{ type: "text", text: JSON.stringify({ entries }, null, 2) }],
        };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  // ── Data Transfer ──────────────────────────────────────────────

  server.tool(
    "sandbox_upload",
    "Upload a file into the sandbox from a local path or URL.",
    {
      sandbox_id: z.string().describe("Sandbox ID"),
      source: z.string().describe("Local file path or URL"),
      destination: z.string().describe("Destination path inside sandbox"),
    },
    async ({ sandbox_id, source, destination }) => {
      try {
        const sandbox = await getSandbox(sandbox_id);

        if (source.startsWith("http://") || source.startsWith("https://")) {
          // URL: let the sandbox fetch it directly
          const escDest = destination.replace(/'/g, "'\\''");
          const escUrl = source.replace(/'/g, "'\\''");
          const result = await sandbox.process.exec(
            `curl -fsSL -o '${escDest}' '${escUrl}'`,
            { timeout: 60 },
          );
          if (result.exitCode !== 0) {
            return errorResult(new Error(`Download failed: ${result.stderr}`));
          }
          const stat = await sandbox.process.exec(`stat -c %s '${escDest}'`);
          const size = parseInt(stat.stdout.trim(), 10);
          return {
            content: [{ type: "text", text: JSON.stringify({ path: destination, bytes_written: size }) }],
          };
        }

        // Local file: read and push through SDK
        const file = Bun.file(source);
        if (!(await file.exists())) {
          return errorResult(new McpError("file_not_found", `Local file not found: ${source}`, "Check the file path."));
        }
        const data = new Uint8Array(await file.arrayBuffer());
        await sandbox.fs.write(destination, data);
        return {
          content: [{ type: "text", text: JSON.stringify({ path: destination, bytes_written: data.byteLength }) }],
        };
      } catch (e) {
        return errorResult(e);
      }
    },
  );

  server.tool(
    "sandbox_download",
    "Download a file from the sandbox to the host",
    {
      sandbox_id: z.string().describe("Sandbox ID"),
      path: z.string().describe("Path inside sandbox"),
      destination: z.string().describe("Relative path under the current working directory to write the file to"),
    },
    async ({ sandbox_id, path: filePath, destination }) => {
      try {
        const sandbox = await getSandbox(sandbox_id);
        const data = await sandbox.fs.read(filePath);

        const cwd = process.cwd();
        const raw = destination;

        // Resolve relative to cwd; reject absolute paths or traversals that escape it.
        const resolved = raw.startsWith("/") ? raw : `${cwd}/${raw}`;
        const { resolve } = await import("node:path");
        const localPath = resolve(resolved);
        if (!localPath.startsWith(cwd + "/") && localPath !== cwd) {
          return errorResult(new McpError("invalid_path", "Download destination must be within the current working directory.", "Use a relative path."));
        }

        // Ensure parent directory exists
        const dir = localPath.substring(0, localPath.lastIndexOf("/"));
        if (dir) {
          const proc = Bun.spawn(["mkdir", "-p", dir], { stdout: "ignore", stderr: "ignore" });
          await proc.exited;
        }

        await Bun.write(localPath, data);
        return {
          content: [{ type: "text", text: JSON.stringify({ local_path: localPath, size: data.byteLength }) }],
        };
      } catch (e) {
        return errorResult(e);
      }
    },
  );
}
