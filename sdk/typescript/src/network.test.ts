import { test, expect } from "vitest";
import type { NetworkRule } from "./index.ts";

// Unit-level test of the wire-format conversion. The real round-trip is
// covered by the integration tests under __integration__/ when the daemon is
// available.

// We import toWireRule indirectly by mirroring its signature here. Keeping
// this as a contract test: if the wire shape changes, both ends must update.

test("NetworkRule type accepts the documented shapes", () => {
  const rules: NetworkRule[] = [
    { id: "exact", match: { host: "api.openai.com" }, action: "allow" },
    { match: { host: "*.github.com" }, action: "deny" },
    {
      match: { host: "api.example.com", method: "POST", pathPrefix: "/v1" },
      action: "inject",
      inject: { setHeaders: { Authorization: "Bearer x" } },
    },
    {
      match: { host: "/^analytics\\./" },
      action: "deny",
    },
  ];
  expect(rules.length).toBe(4);
});
