import type { ComponentChildren } from "preact";
import { useEffect, useState } from "preact/hooks";
import { getHealth, getSession, logout, type Health } from "../api";

/** Layout is the app shell: nav, schema-mismatch banner and a logout button. */
export function Layout({ children }: { children: ComponentChildren }) {
  const [health, setHealth] = useState<Health | null>(null);
  const [user, setUser] = useState("");
  useEffect(() => {
    getHealth().then(setHealth).catch(() => {});
    getSession().then((s) => setUser(s.username)).catch(() => {});
  }, []);
  return (
    <>
      <header class="topbar">
        <strong>phins-news admin</strong>
        <nav>
          <a href="/">Dashboard</a>
          <a href="/actions">Actions</a>
          <a href="/jobs">Jobs</a>
          <a href="/audit">Audit</a>
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
