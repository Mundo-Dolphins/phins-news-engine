import { useEffect, useLayoutEffect, useRef, useState } from "preact/hooks";
import { useShortcutIsolation } from "./reviewParts";

/** ConfirmDialog makes the user type the action name before a destructive run. */
export function ConfirmDialog({ name, onConfirm, onCancel }: { name: string; onConfirm: () => void; onCancel: () => void }) {
  const [typed, setTyped] = useState("");
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onCancel();
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onCancel]);
  return (
    <div class="overlay" role="presentation">
      <div class="dialog" role="dialog" aria-modal="true" aria-labelledby="confirm-title">
        <h2 id="confirm-title">Confirm destructive action</h2>
        <p>
          This cannot be undone. Type <code>{name}</code> to confirm.
        </p>
        <input aria-label="Action name" value={typed} autofocus onInput={(e) => setTyped((e.target as HTMLInputElement).value)} />
        <div class="row end">
          <button type="button" class="btn" onClick={onCancel}>
            Cancel
          </button>
          <button type="button" class="btn danger" disabled={typed !== name} onClick={onConfirm}>
            Confirm
          </button>
        </div>
      </div>
    </div>
  );
}

/** DestructiveConfirm is `ConfirmDialog` with no key press reaching the review's shortcuts. */
export function DestructiveConfirm({ name, onConfirm, onCancel }: { name: string; onConfirm(): void; onCancel(): void }) {
  const root = useRef<HTMLDivElement>(null);
  const field = useRef<HTMLElement | null>(null);
  useLayoutEffect(() => {
    field.current = root.current?.querySelector("input") ?? null;
  }, []);
  useShortcutIsolation(root, field, onCancel);
  const onKeyDown = (e: KeyboardEvent) => {
    e.stopPropagation();
    if (e.key === "Escape") onCancel();
  };
  return (
    <div ref={root} onKeyDown={onKeyDown}>
      <ConfirmDialog name={name} onConfirm={onConfirm} onCancel={onCancel} />
    </div>
  );
}
