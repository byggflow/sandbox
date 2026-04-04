export type Auth =
  | string
  | Record<string, string>
  | (() => Promise<Record<string, string>>);

export function resolveAuth(auth: Auth | undefined): () => Promise<Record<string, string>> {
  if (!auth) return async () => ({});
  if (typeof auth === "string") return async () => ({ Authorization: `Bearer ${auth}` });
  if (typeof auth === "function") return auth;
  return async () => auth;
}
