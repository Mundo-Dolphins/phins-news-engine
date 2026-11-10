import type { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";
import { getSession } from "../api";

/**
 * LocalOnly renders its children only in local mode. The review actions are
 * local-only server-side; this keeps production from showing a raw not_found.
 * If the session cannot be read the children render and the server decides.
 */
export function LocalOnly({ children }: { children: ComponentChildren }) {
  const [mode, setMode] = useState<string | null>(null);
  useEffect(() => {
    getSession()
      .then((s) => setMode(s.mode))
      .catch(() => setMode("local"));
  }, []);
  if (mode === null) return <p class="muted">Loading…</p>;
  if (mode !== "local") return <p class="muted">Review screens are only available in local mode.</p>;
  return <>{children}</>;
}
