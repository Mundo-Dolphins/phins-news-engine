import { ApiError } from "../api";

/** errorText renders an error for a notice, with the server's code when there is one. */
export function errorText(e: unknown): string {
  if (e instanceof ApiError) return `${e.message} (${e.code})`;
  return e instanceof Error ? e.message : String(e);
}

/** when formats an ISO timestamp for a review card; an unparsable value is shown as is. */
export function when(iso: string): string {
  if (!iso) return "";
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}
