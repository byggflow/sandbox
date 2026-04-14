/**
 * E2E encryption integration tests for binary file transfers.
 *
 * Validates that binary frame payloads (fs.read, fs.write, fs.upload, fs.download)
 * are encrypted when using { encrypted: true }.
 *
 *   SANDBOXD_ENDPOINT=http://localhost:7522 bunx --bun vitest run src/__integration__/e2e.integration.test.ts
 */
import { describe, test, expect, afterEach } from "vitest";
import { createSandbox, type Sandbox } from "../index.ts";

const ENDPOINT = process.env.SANDBOXD_ENDPOINT ?? "";
const skip = !ENDPOINT;

describe.skipIf(skip)("e2e encryption: binary file transfers", () => {
  const sandboxes: Sandbox[] = [];

  afterEach(async () => {
    for (const sbx of sandboxes) {
      try {
        await sbx.close();
      } catch {
        // best effort
      }
    }
    sandboxes.length = 0;
  });

  async function createEncrypted(): Promise<Sandbox> {
    const sbx = await createSandbox({ endpoint: ENDPOINT, encrypted: true });
    sandboxes.push(sbx);
    return sbx;
  }

  test("write and read small file", async () => {
    const sbx = await createEncrypted();
    const content = "hello encrypted world";

    await sbx.fs.write("/tmp/e2e-test.txt", content);
    const data = await sbx.fs.read("/tmp/e2e-test.txt");

    expect(new TextDecoder().decode(data)).toBe(content);
  });

  test("write and read binary data", async () => {
    const sbx = await createEncrypted();
    // Create binary data with all byte values.
    const binary = new Uint8Array(256);
    for (let i = 0; i < 256; i++) binary[i] = i;

    await sbx.fs.write("/tmp/e2e-binary.bin", binary);
    const data = await sbx.fs.read("/tmp/e2e-binary.bin");

    expect(data).toEqual(binary);
  });

  test("write and read file larger than chunk threshold", { timeout: 30_000 }, async () => {
    const sbx = await createEncrypted();
    // 2.5 MB file -- exceeds the 1 MB chunk threshold, requiring multiple
    // encrypted binary frames in both directions.
    const size = 2.5 * 1024 * 1024;
    const large = new Uint8Array(size);
    for (let i = 0; i < large.length; i++) large[i] = i & 0xff;

    await sbx.fs.write("/tmp/e2e-large.bin", large);
    const data = await sbx.fs.read("/tmp/e2e-large.bin");

    expect(data.byteLength).toBe(large.byteLength);
    expect(data).toEqual(large);
  });

  test("write and read file at chunk boundary", async () => {
    const sbx = await createEncrypted();
    // Exactly 1 MB -- sits at the chunked/non-chunked boundary.
    const size = 1024 * 1024;
    const exact = new Uint8Array(size);
    for (let i = 0; i < exact.length; i++) exact[i] = i & 0xff;

    await sbx.fs.write("/tmp/e2e-exact.bin", exact);
    const data = await sbx.fs.read("/tmp/e2e-exact.bin");

    expect(data.byteLength).toBe(exact.byteLength);
    expect(data).toEqual(exact);
  });

  test("upload and download directory (tar)", async () => {
    const sbx = await createEncrypted();

    // Write a few files, then download as tar.
    await sbx.fs.mkdir("/tmp/e2e-dir");
    await sbx.fs.write("/tmp/e2e-dir/a.txt", "file a");
    await sbx.fs.write("/tmp/e2e-dir/b.txt", "file b");

    const tar = await sbx.fs.download("/tmp/e2e-dir");
    expect(tar.byteLength).toBeGreaterThan(0);

    // Upload the tar to a new directory and verify contents.
    await sbx.fs.upload("/tmp/e2e-dir-copy", tar);

    const a = await sbx.fs.read("/tmp/e2e-dir-copy/a.txt");
    const b = await sbx.fs.read("/tmp/e2e-dir-copy/b.txt");
    expect(new TextDecoder().decode(a)).toBe("file a");
    expect(new TextDecoder().decode(b)).toBe("file b");
  });

  test("encrypted exec still works alongside binary ops", async () => {
    const sbx = await createEncrypted();
    await sbx.fs.write("/tmp/e2e-exec.txt", "exec test");

    const result = await sbx.process.exec("cat /tmp/e2e-exec.txt");
    expect(result.stdout).toBe("exec test");
    expect(result.exitCode).toBe(0);
  });

  test("empty file round-trips correctly", async () => {
    const sbx = await createEncrypted();
    await sbx.fs.write("/tmp/e2e-empty.txt", "");
    const data = await sbx.fs.read("/tmp/e2e-empty.txt");
    expect(data.byteLength).toBe(0);
  });
});
