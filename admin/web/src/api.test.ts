import { listActions, runAction, ApiError } from "./api";

describe("api", () => {
  const assign = vi.fn();
  beforeEach(() => {
    assign.mockReset();
    vi.stubGlobal("location", { pathname: "/actions/x", search: "?a=1", assign });
  });
  afterEach(() => vi.unstubAllGlobals());

  it("redirects to /login?next= on 401", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { code: "unauthorized", message: "no" } }), { status: 401 })));
    await expect(listActions()).rejects.toBeInstanceOf(ApiError);
    expect(assign).toHaveBeenCalledWith("/login?next=" + encodeURIComponent("/actions/x?a=1"));
  });

  it("posts params and confirm, returning a job for 202", async () => {
    const f = vi.fn(async () => new Response(JSON.stringify({ job: { id: "j1" } }), { status: 202 }));
    vi.stubGlobal("fetch", f);
    const out = await runAction("train.run", { a: 1 }, true);
    expect(out).toEqual({ job: { id: "j1" } });
    const [url, init] = f.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("/api/actions/train.run");
    expect(JSON.parse(init.body as string)).toEqual({ params: { a: 1 }, confirm: true });
  });

  it("surfaces the error envelope", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ error: { code: "invalid_params", message: "bad" } }), { status: 400 })));
    await expect(runAction("x", {}, false)).rejects.toMatchObject({ code: "invalid_params", message: "bad", status: 400 });
  });
});
