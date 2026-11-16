import { useEffect, useRef, useState } from "preact/hooks";
import { clearLLM, getLLMModels, getLLMStatus, selectLLM, type LLMStatus } from "../api";
import { LocalOnly } from "../components/LocalOnly";

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

/** LLMSettings picks the LLM server and model the local pipeline uses (in memory only). */
export function LLMSettings() {
  return (
    <LocalOnly>
      <LLMSettingsScreen />
    </LocalOnly>
  );
}

function LLMSettingsScreen() {
  const [status, setStatus] = useState<LLMStatus | null>(null);
  const [error, setError] = useState("");
  const [server, setServer] = useState("");
  const [models, setModels] = useState<string[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [unavailable, setUnavailable] = useState("");
  const [model, setModel] = useState("");
  const [busy, setBusy] = useState(false);
  // latest holds the most recently picked server; slower responses for an earlier pick are dropped.
  const latest = useRef("");

  useEffect(() => {
    getLLMStatus().then(setStatus).catch((e) => setError(message(e)));
  }, []);

  const pickServer = async (name: string) => {
    latest.current = name;
    setServer(name);
    setLoading(false);
    setModels(null);
    setModel("");
    setUnavailable("");
    if (!name) return;
    setLoading(true);
    try {
      const list = await getLLMModels(name);
      if (latest.current === name) setModels(list);
    } catch (e) {
      if (latest.current === name) setUnavailable(message(e));
    } finally {
      if (latest.current === name) setLoading(false);
    }
  };

  const apply = async (run: () => Promise<LLMStatus>) => {
    setBusy(true);
    setError("");
    try {
      setStatus(await run());
    } catch (e) {
      setError(message(e));
    } finally {
      setBusy(false);
    }
  };

  const sel = status?.selection;
  return (
    <section>
      <h1>LLM</h1>
      {error && (
        <p class="error" role="alert">
          {error}
        </p>
      )}
      {status && <p>{sel ? `Using ${sel.server} · ${sel.model}` : "Using settings.yml defaults"}</p>}
      {status && (
        <>
          <label>
            Server
            <select value={server} onChange={(e) => pickServer((e.target as HTMLSelectElement).value)}>
              <option value="">Select a server</option>
              {status.servers.map((s) => (
                <option key={s.name} value={s.name}>
                  {s.name}
                </option>
              ))}
            </select>
          </label>
          {loading && <p class="muted">Loading models…</p>}
          {unavailable && (
            <p class="error" role="alert">
              Server unavailable: {unavailable}
            </p>
          )}
          {models && (
            <label>
              Model
              <select value={model} onChange={(e) => setModel((e.target as HTMLSelectElement).value)}>
                <option value="">Select a model</option>
                {models.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            </label>
          )}
          <p>
            <button class="btn" disabled={!server || !model || loading || busy} onClick={() => apply(() => selectLLM(server, model))}>
              Save
            </button>{" "}
            {sel && (
              <button class="btn" disabled={busy} onClick={() => apply(clearLLM)}>
                Reset to settings.yml
              </button>
            )}
          </p>
        </>
      )}
    </section>
  );
}
