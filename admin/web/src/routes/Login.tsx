import { useState } from "preact/hooks";
import { ApiError, login } from "../api";
import { safeNext } from "../redirect";

export function Login() {
  const [error, setError] = useState("");
  const submit = async (e: Event) => {
    e.preventDefault();
    const f = new FormData(e.target as HTMLFormElement);
    try {
      await login(String(f.get("username")), String(f.get("password")));
      location.assign(safeNext(new URLSearchParams(location.search).get("next"), location.origin));
    } catch (err) {
      setError(err instanceof ApiError && err.status === 429 ? "Too many attempts, wait a minute." : err instanceof Error ? err.message : "Login failed");
    }
  };
  return (
    <main class="login">
      <form class="schema-form" onSubmit={submit}>
        <h1>phins-news admin</h1>
        <div class="field">
          <label for="u">Username</label>
          <input id="u" name="username" autocomplete="username" required />
        </div>
        <div class="field">
          <label for="p">Password</label>
          <input id="p" name="password" type="password" autocomplete="current-password" required />
        </div>
        {error && <p class="error" role="alert">{error}</p>}
        <button class="btn primary" type="submit">Log in</button>
      </form>
    </main>
  );
}
