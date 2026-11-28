import { useEffect, useState } from "preact/hooks";
import { applyConfig, listActions, listConfigFiles, readConfig, type ConfigDoc, type ConfigFile } from "../api";
import { LocalOnly } from "../components/LocalOnly";
import { ConfigForm } from "../components/ConfigForm";
import { computeEdits, removeAt, setAt } from "../components/configTree";

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

// The config editor runs on actions the host registers (config.files/read/apply);
// the engine registers none. Their presence in the catalog turns the screen on.
const CONFIG_FILES = "config.files";

/** hostHasConfigEditor reports whether the host registered the config editor actions. */
export async function hostHasConfigEditor(): Promise<boolean> {
  return (await listActions()).some((a) => a.name === CONFIG_FILES);
}

/** Config is the local-mode settings editor: a structured form per file that
 * writes only the changed leaves back, preserving every comment. It shows only
 * when the host registers the config.* actions, as the dictionary review does. */
export function Config() {
  return (
    <LocalOnly>
      <HostGate />
    </LocalOnly>
  );
}

function HostGate() {
  const [state, setState] = useState<{ ok: boolean | null; error: string }>({ ok: null, error: "" });
  useEffect(() => {
    hostHasConfigEditor()
      .then((ok) => setState({ ok, error: "" }))
      .catch((e) => setState({ ok: false, error: message(e) }));
  }, []);
  if (state.ok === null) return <p class="muted">Loading…</p>;
  if (state.ok) return <ConfigScreen />;
  return (
    <>
      <h1>Configuration</h1>
      {state.error ? (
        <p class="error" role="alert">
          Could not read the action catalog: {state.error}
        </p>
      ) : (
        <>
          <p>Not available on this host</p>
          <p class="muted">This editor runs on the config.* actions the host registers; this host registers none.</p>
        </>
      )}
    </>
  );
}

function ConfigScreen() {
  const [files, setFiles] = useState<ConfigFile[] | null>(null);
  const [key, setKey] = useState("");
  const [doc, setDoc] = useState<ConfigDoc | null>(null);
  const [original, setOriginal] = useState<any>(null);
  const [tree, setTree] = useState<any>(null);
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listConfigFiles()
      .then((fs) => {
        setFiles(fs);
        const first = fs.find((f) => f.exists) || fs[0];
        if (first) setKey(first.key);
      })
      .catch((e) => setError(message(e)));
  }, []);

  useEffect(() => {
    if (!key) return;
    setDoc(null);
    setTree(null);
    setOriginal(null);
    setSaved("");
    setBusy(true);
    readConfig(key)
      .then((d) => {
        setDoc(d);
        setOriginal(d.value);
        setTree(d.value);
      })
      .catch((e) => setError(message(e)))
      .finally(() => setBusy(false));
  }, [key]);

  const onSet = (path: string[], value: any) => setTree((t: any) => setAt(t, path, value));
  const onRemove = (path: string[]) => setTree((t: any) => removeAt(t, path));

  const edits = tree != null && original != null ? computeEdits(original, tree) : [];
  const dirty = edits.length > 0;

  const save = async () => {
    setBusy(true);
    setError("");
    setSaved("");
    try {
      const res = await applyConfig(key, edits);
      setSaved(`Saved ${res.edits} change(s). Backup at ${res.backup}.`);
      const d = await readConfig(key);
      setDoc(d);
      setOriginal(d.value);
      setTree(d.value);
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };

  const reset = () => {
    setTree(original);
    setSaved("");
    setError("");
  };

  return (
    <section>
      <h1>Configuration</h1>
      <p class="muted">
        Edit the settings and policy files as forms. Comments (the rationale in the YAML) are preserved; only the
        fields you change are written, and the file is validated before it is saved.
      </p>
      {files && (
        <label>
          File{" "}
          <select value={key} onChange={(e) => setKey((e.target as HTMLSelectElement).value)}>
            {files.map((f) => (
              <option key={f.key} value={f.key} disabled={!f.exists}>
                {f.title}
                {f.exists ? "" : " (not found)"}
              </option>
            ))}
          </select>
        </label>
      )}
      {error && (
        <p class="error" role="alert">
          {error}
        </p>
      )}
      {saved && <p class="ok">{saved}</p>}
      {!busy && doc && tree != null && (
        <div class="cfg-root">
          <ConfigForm value={tree} path={[]} comments={doc.comments} onSet={onSet} onRemove={onRemove} />
        </div>
      )}
      {doc && (
        <p class="cfg-actions">
          <button class="btn" disabled={!dirty || busy} onClick={save}>
            Save {dirty ? `${edits.length} change${edits.length === 1 ? "" : "s"}` : ""}
          </button>{" "}
          <button class="btn" disabled={!dirty || busy} onClick={reset}>
            Reset
          </button>
        </p>
      )}
      {busy && <p class="muted">Loading…</p>}
    </section>
  );
}
