import type { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";
import { getHealth, getLLMStatus, getSession, LLM_CHANGED, logout, type Health, type LLMSelection } from "../api";

/** Layout is the app shell: nav, schema-mismatch banner and a logout button. */
export function Layout({ children }: { children: ComponentChildren }) {
  const [health, setHealth] = useState<Health | null>(null);
  const [user, setUser] = useState("");
  const [mode, setMode] = useState("");
  const [llm, setLLM] = useState<LLMSelection | null>(null);
  useEffect(() => {
    getHealth().then(setHealth).catch(() => {});
    getSession()
      .then((s) => {
        setUser(s.username);
        setMode(s.mode);
      })
      .catch(() => {});
  }, []);
  useEffect(() => {
    if (mode !== "local") return;
    const refresh = () =>
      getLLMStatus()
        .then((s) => setLLM(s.selection))
        .catch(() => setLLM(null));
    refresh();
    window.addEventListener(LLM_CHANGED, refresh);
    return () => window.removeEventListener(LLM_CHANGED, refresh);
  }, [mode]);
  return (
    <>
      <header class="topbar">
        <strong>phins-news admin</strong>
        <nav>
          <a href="/">Dashboard</a>
          <a href="/actions">Actions</a>
          <a href="/jobs">Jobs</a>
          <a href="/audit">Audit</a>
          {mode === "local" && <a href="/review">Review</a>}
          {mode === "local" && <a href="/cycle">Editorial cycle</a>}
          {mode === "local" && <a href="/settings/llm">LLM{llm && ` (${llm.server} · ${llm.model})`}</a>}
        </nav>
        <span class="spacer" />
        {health && <span class={"badge mode-" + health.mode}>{health.mode}</span>}
        {user && (
          <button
            class="btn"
            onClick={async () => {
              await logout();
              location.assign("/login");
            }}
          >
            Log out ({user})
          </button>
        )}
      </header>
      {health?.schema === "mismatch" && (
        <div class="banner" role="alert">
          Writes disabled: operational DB schema differs from this binary
        </div>
      )}
      <main>{children}</main>
    </>
  );
}
