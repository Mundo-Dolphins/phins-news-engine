import { safeNext } from "./redirect";

describe("safeNext", () => {
  const o = "http://127.0.0.1:8480";
  it.each(["/\\evil.com", "//evil.com", "https://evil.com", "javascript:alert(1)", null, ""])(
    "rejects %s",
    (n) => expect(safeNext(n, o)).toBe("/"),
  );
  it("keeps same-origin paths", () => {
    expect(safeNext("/actions/claims.list?x=1", o)).toBe("/actions/claims.list?x=1");
  });
});
