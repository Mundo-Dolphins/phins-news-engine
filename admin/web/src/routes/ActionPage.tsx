import { useEffect, useState } from "preact/hooks";
import { useRoute, useLocation } from "preact-iso";
import { ApiError, listActions, runAction, type CatalogItem, type Result } from "../api";
import { SchemaForm } from "../components/SchemaForm";
import { ResultView } from "../components/ResultView";
import { ConfirmDialog } from "../components/ConfirmDialog";

export function KindBadge({ kind }: { kind: string }) {
  return <span class={"badge kind-" + kind}>{kind}</span>;
}

export function ActionPage() {
  const { params } = useRoute();
  const { route } = useLocation();
  const name = params.name;
  const [action, setAction] = useState<CatalogItem | null>(null);
  const [pending, setPending] = useState<Record<string, unknown> | null>(null);
  const [result, setResult] = useState<Result | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setAction(null);
    setResult(null);
    setError("");
    listActions()
      .then((as) => {
        const a = as.find((x) => x.name === name);
        if (a) setAction(a);
        else setError("Unknown action (or not available in this mode).");
      })
      .catch((e) => setError(e.message));
  }, [name]);

  const run = async (p: Record<string, unknown>, confirm: boolean) => {
    setBusy(true);
    setError("");
    setResult(null);
    try {
      const out = await runAction(name, p, confirm);
      if ("job" in out) route("/jobs/" + out.job.id);
      else setResult(out.result);
    } catch (e) {
      setError(e instanceof ApiError ? `${e.message} (${e.code})` : String(e));
    } finally {
      setBusy(false);
    }
  };

  const submit = (p: Record<string, unknown>) => {
    if (action?.kind === "destructive") setPending(p);
    else run(p, false);
  };

  if (!action) return <>{error ? <p class="error" role="alert">{error}</p> : <p class="muted">Loading…</p>}</>;
  return (
    <>
      <h1>{action.title}</h1>
      <p>
        <code>{action.name}</code> <KindBadge kind={action.kind} />
        {action.long_running && <span class="badge">long-running</span>}
        {action.exclusive && <span class="badge">exclusive</span>}
      </p>
      <p>{action.description}</p>
      <SchemaForm schema={action.schema} onSubmit={submit} busy={busy} />
      {error && <p class="error" role="alert">{error}</p>}
      {result && <ResultView result={result} />}
      {pending && (
        <ConfirmDialog
          name={action.name}
          onCancel={() => setPending(null)}
          onConfirm={() => {
            const p = pending;
            setPending(null);
            run(p, true);
          }}
        />
      )}
    </>
  );
}
