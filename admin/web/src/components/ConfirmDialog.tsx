import { useEffect, useState } from "preact/hooks";

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
