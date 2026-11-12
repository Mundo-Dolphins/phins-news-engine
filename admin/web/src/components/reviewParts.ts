import { ApiError } from "../api";
import type { RefObject } from "preact";
import { useLayoutEffect, useRef } from "preact/hooks";

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

/**
 * useShortcutIsolation keeps every key press away from page-level shortcuts
 * while the panel `root` is mounted, as `MergePicker` does: a key outside
 * the panel is swallowed (Escape calls `onEscape`, any other sends focus to
 * `focusTarget`). Keys inside the panel must be stopped by the panel's own
 * keydown handler.
 */
export function useShortcutIsolation(
  root: RefObject<HTMLElement | null>,
  focusTarget: RefObject<HTMLElement | null>,
  onEscape: () => void,
): void {
  const escapeRef = useRef(onEscape);
  escapeRef.current = onEscape;
  useLayoutEffect(() => {
    // Capture on window runs before any page-level keydown listener.
    const guard = (e: KeyboardEvent) => {
      if (root.current?.contains(e.target as Node)) return;
      e.stopPropagation();
      e.preventDefault();
      if (e.key === "Escape") escapeRef.current();
      else focusTarget.current?.focus();
    };
    window.addEventListener("keydown", guard, true);
    return () => window.removeEventListener("keydown", guard, true);
  }, []);
}
