/** safeNext resolves a post-login ?next= target, allowing same-origin paths only. */
export function safeNext(next: string | null, origin: string): string {
  if (!next) return "/";
  try {
    const u = new URL(next, origin);
    if (u.origin !== origin) return "/";
    return u.pathname + u.search + u.hash;
  } catch {
    return "/";
  }
}
