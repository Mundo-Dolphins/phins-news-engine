import { safeHref } from "./links";

describe("safeHref", () => {
  it("keeps http and https URLs", () => {
    expect(safeHref("https://example.com/a?b=1")).toBe("https://example.com/a?b=1");
    expect(safeHref("http://example.com/")).toBe("http://example.com/");
  });

  it("rejects other schemes, empty and relative URLs", () => {
    expect(safeHref("javascript:alert(1)")).toBeNull();
    expect(safeHref(" JavaScript:alert(1)")).toBeNull();
    expect(safeHref("data:text/html,x")).toBeNull();
    expect(safeHref("")).toBeNull();
    expect(safeHref("   ")).toBeNull();
    expect(safeHref("/relative/path")).toBeNull();
    expect(safeHref("example.com/page")).toBeNull();
  });
});
