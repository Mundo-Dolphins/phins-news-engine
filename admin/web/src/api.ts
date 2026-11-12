// Typed client for the admin REST API. Any 401 sends the browser to the
// login page with the current location as ?next=.

export interface Prop {
  type: "string" | "boolean" | "integer" | "number" | "array";
  description?: string;
  default?: unknown;
  enum?: string[];
  format?: string;
  items?: { type: string };
}
export interface Schema {
  type: "object";
  properties: Record<string, Prop>;
  required?: string[];
  additionalProperties?: boolean;
}
export interface CatalogItem {
  name: string;
  group: string;
  title: string;
  description: string;
  kind: "read" | "write" | "destructive";
  modes: string[];
  long_running: boolean;
  exclusive: boolean;
  schema: Schema;
}
export interface Column {
  key: string;
  label: string;
}
export interface Result {
  render: "table" | "object" | "text";
  columns?: Column[];
  rows?: Record<string, unknown>[];
  object?: unknown;
  text?: string;
  summary?: string;
}
export interface Job {
  id: string;
  action: string;
  actor: string;
  status: string;
  params?: unknown;
  result?: Result;
  error?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
}
export interface AuditEntry {
  id: number;
  at: string;
  actor: string;
  action: string;
  kind: string;
  outcome: string;
  error?: string;
  params?: unknown;
}
export interface LogLine {
  seq: number;
  at: string;
  line: string;
}
export interface Health {
  status: string;
  mode: string;
  schema: "ok" | "mismatch";
}
export type RunOutcome = { result: Result } | { job: Job };

/** ApiError carries the server's error envelope. */
export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    credentials: "same-origin",
    headers: body === undefined ? undefined : { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 401 && path !== "/api/session") {
    location.assign("/login?next=" + encodeURIComponent(location.pathname + location.search));
  }
  if (!res.ok) {
    let code = "http_" + res.status;
    let message = res.statusText || "request failed";
    try {
      const e = (await res.json()).error;
      if (e) {
        code = e.code;
        message = e.message;
      }
    } catch {
      /* not JSON */
    }
    throw new ApiError(res.status, code, message);
  }
  const text = await res.text();
  return (text ? JSON.parse(text) : undefined) as T;
}

export const getSession = () => call<{ username: string; mode: string }>("GET", "/api/session");
export const login = (username: string, password: string) =>
  call<void>("POST", "/api/session", { username, password });
export const logout = () => call<void>("DELETE", "/api/session");
export const getHealth = () => call<Health>("GET", "/healthz");
export const listActions = () => call<CatalogItem[]>("GET", "/api/actions");
export const runAction = (name: string, params: Record<string, unknown>, confirm: boolean) =>
  call<RunOutcome>("POST", "/api/actions/" + encodeURIComponent(name), { params, confirm });
export const listJobs = () => call<Job[]>("GET", "/api/jobs");
export const getJob = (id: string) => call<Job>("GET", "/api/jobs/" + encodeURIComponent(id));
export const cancelJob = (id: string) => call<void>("POST", "/api/jobs/" + encodeURIComponent(id) + "/cancel");
export const listAudit = () => call<AuditEntry[]>("GET", "/api/audit");

/**
 * jobEvents streams a job's log; returns a function that closes the stream.
 * When onError is given, a stream error closes the stream and calls it
 * (without it the browser keeps reconnecting).
 */
export function jobEvents(
  id: string,
  onLine: (l: LogLine) => void,
  onEnd: (j: Job) => void,
  onError?: () => void,
): () => void {
  const es = new EventSource("/api/jobs/" + encodeURIComponent(id) + "/events");
  es.addEventListener("log", (e) => onLine(JSON.parse((e as MessageEvent).data)));
  es.addEventListener("end", (e) => {
    onEnd(JSON.parse((e as MessageEvent).data));
    es.close();
  });
  if (onError) {
    es.onerror = () => {
      es.close();
      onError();
    };
  }
  return () => es.close();
}
