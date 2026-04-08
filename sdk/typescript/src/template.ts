import type { Auth } from "./auth.ts";
import { resolveAuth } from "./auth.ts";
import { DEFAULT_ENDPOINT } from "./sandbox.ts";

/** Client for listing, fetching, and deleting sandbox templates. */
export interface TemplateManager {
  list(): Promise<{ id: string; label: string }[]>;
  get(id: string): Promise<{ id: string; label: string }>;
  delete(id: string): Promise<void>;
}

export interface TemplateOptions {
  endpoint?: string;
  auth?: Auth;
}

/** Resolve HTTP endpoint from raw endpoint string. */
function resolveHttpEndpoint(endpoint: string): string {
  if (endpoint.startsWith("unix://")) {
    return "http://localhost:7522";
  }
  return endpoint.replace(/\/$/, "");
}

/** Create a template manager for listing, fetching, and deleting templates. */
export function templates(opts?: TemplateOptions): TemplateManager {
  const endpoint = opts?.endpoint ?? DEFAULT_ENDPOINT;
  const httpBase = resolveHttpEndpoint(endpoint);
  const authResolver = resolveAuth(opts?.auth);

  return {
    async list(): Promise<{ id: string; label: string }[]> {
      const headers = await authResolver();
      const response = await fetch(`${httpBase}/templates`, {
        headers: { ...headers },
      });
      if (!response.ok) {
        throw new Error(`Failed to list templates: ${response.status}`);
      }
      return response.json() as Promise<{ id: string; label: string }[]>;
    },

    async get(id: string): Promise<{ id: string; label: string }> {
      const headers = await authResolver();
      const response = await fetch(`${httpBase}/templates/${id}`, {
        headers: { ...headers },
      });
      if (!response.ok) {
        throw new Error(`Failed to get template: ${response.status}`);
      }
      return response.json() as Promise<{ id: string; label: string }>;
    },

    async delete(id: string): Promise<void> {
      const headers = await authResolver();
      const response = await fetch(`${httpBase}/templates/${id}`, {
        method: "DELETE",
        headers: { ...headers },
      });
      if (!response.ok) {
        throw new Error(`Failed to delete template: ${response.status}`);
      }
    },
  };
}
