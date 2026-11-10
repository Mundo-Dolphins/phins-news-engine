/**
 * safeHref returns url when it is an absolute http: or https: URL, and null
 * otherwise (empty, relative, javascript:, data:, …). Only a non-null result
 * may become an <a href>; anything else is shown as plain text.
 */
export function safeHref(url: string): string | null {
  const s = url.trim();
  if (!s) return null;
  let parsed: URL;
  try {
    parsed = new URL(s);
  } catch {
    return null;
  }
  return parsed.protocol === "http:" || parsed.protocol === "https:" ? s : null;
}
