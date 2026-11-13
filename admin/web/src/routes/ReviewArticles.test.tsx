import { render, screen, fireEvent, waitFor, within } from "@testing-library/preact";
import { ApiError, getSession, runAction, type RunOutcome } from "../api";
import { ReviewArticles } from "./ReviewArticles";

vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  runAction: vi.fn(),
  getSession: vi.fn(),
}));
const run = vi.mocked(runAction);

const obj = (object: unknown, summary = ""): RunOutcome => ({ result: { render: "object", object, summary } });

function cycleState(step: string | null, status = "pending") {
  return obj({
    cycle:
      step === null
        ? null
        : {
            cycle_id: "20261113T090000-0a0b0c0d",
            started_at: "2026-11-13T09:00:00Z",
            step,
            session_id: "sess-1",
            steps: { [step]: { status, result: null, error: "", updated_at: "2026-11-13T09:10:00Z" } },
            sample_size: 0,
            entities_verification: "local",
          },
    available: { sync: true, telegram: true },
    order: ["sync", "telegram_import", "roster_sync", "article_review", "entities", "event_rules", "mined_terms", "training", "summary"],
    skippable: [],
    disabled: {},
  });
}

const LONG = "First paragraph of a long summary. ".repeat(30) + "The very last sentence of the summary.";

const other = (i: number) => ({
  article_id: "m" + i,
  title: "Cluster mate headline " + i,
  source: "Feed " + i,
  url: "https://news.example.com/mate-" + i,
  published_at: "2026-11-12T08:00:00Z",
});

type Decision = { action: string; notes: string } | null;

function article(id: string, index: number, over: Record<string, unknown> = {}) {
  return {
    article_id: id,
    index,
    title: "Headline " + id,
    summary: "Summary of " + id,
    url: "https://news.example.com/" + id,
    source: { name: "Test Feed", type: "rss" },
    published_at: "2026-11-12T10:00:00Z",
    score: 0.42,
    ambiguity: 0,
    prior_label: null as { label: string; by: string } | null,
    cluster: { id: "", role: "", others: [] as ReturnType<typeof other>[] },
    decision: null as Decision,
    ...over,
  };
}

type Item = ReturnType<typeof article>;
type Handler = (params: Record<string, unknown>) => Promise<RunOutcome> | RunOutcome;

/** serve answers runAction by name; the queue reflects each review_decide. */
function serve(items: Item[], more: Record<string, Handler> = {}, state = () => cycleState("article_review")) {
  const current = items.map((t) => ({ ...t }));
  const queue = () => obj({ items: current, total: current.length, undecided: current.filter((t) => !t.decision).length });
  run.mockImplementation(async (name, params) => {
    const h = more[name];
    if (h) return h(params);
    switch (name) {
      case "cycle.state":
        return state();
      case "cycle.review_open":
        return obj({ session_id: "sess-1", total: current.length, decided: 0 }, "resuming session sess-1");
      case "cycle.review_queue":
        return queue();
      case "cycle.review_pause":
        return obj({ session_id: "sess-1", status: "paused" });
      case "cycle.review_decide": {
        const t = current.find((x) => x.article_id === params.article_id)!;
        t.decision = { action: params.action as string, notes: "" };
        return obj({ article_id: t.article_id, action: params.action, undecided: current.filter((x) => !x.decision).length });
      }
    }
    throw new Error("unexpected action " + name);
  });
}

const calls = (name: string) => run.mock.calls.filter(([n]) => n === name);
const card = () => screen.getByRole("region", { name: "Selected item" });

async function open(items: Item[], more: Record<string, Handler> = {}, onDone?: () => void) {
  serve(items, more);
  const utils = render(<ReviewArticles onDone={onDone} />);
  await screen.findByRole("heading", { name: "Article review" });
  await waitFor(() => expect(calls("cycle.review_queue").length).toBeGreaterThan(0));
  return utils;
}

describe("ReviewArticles", () => {
  beforeEach(() => {
    run.mockReset();
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
  });

  it("asks to open it from the editorial cycle when no cycle is at the article review", async () => {
    serve([], {}, () => cycleState(null));
    render(<ReviewArticles />);
    expect(await screen.findByText(/Open from the editorial cycle/)).toBeTruthy();
    expect(calls("cycle.review_open").length).toBe(0);
  });

  it("asks to open it from the editorial cycle when the cycle is at another step", async () => {
    serve([], {}, () => cycleState("entities"));
    render(<ReviewArticles />);
    expect(await screen.findByText(/Open from the editorial cycle/)).toBeTruthy();
    expect(calls("cycle.review_open").length).toBe(0);
  });

  it("opens the review before reading the queue and shows the full card with every cluster article", async () => {
    const lead = article("k1", 1, {
      summary: LONG,
      prior_label: { label: "reject", by: "ana" },
      cluster: { id: "c1", role: "primary", others: [1, 2, 3, 4, 5, 6, 7].map(other) },
    });
    await open([lead, article("k2", 2)]);
    const names = run.mock.calls.map(([n]) => n);
    expect(names.indexOf("cycle.review_open")).toBeLessThan(names.indexOf("cycle.review_queue"));
    const c = card();
    expect(within(c).getByRole("heading", { name: "Headline k1" })).toBeTruthy();
    expect(within(c).getByText(LONG.trim())).toBeTruthy();
    const link = within(c).getByRole("link", { name: "https://news.example.com/k1" });
    expect(link.getAttribute("href")).toBe("https://news.example.com/k1");
    expect(link.hasAttribute("data-review-open")).toBe(true);
    for (const i of [1, 2, 3, 4, 5, 6, 7]) {
      const mate = within(c).getByRole("link", { name: "Cluster mate headline " + i });
      expect(mate.getAttribute("href")).toBe("https://news.example.com/mate-" + i);
      expect(within(mate.closest("li")!).getByText(/Feed /)).toBeTruthy();
    }
    expect(within(c).getByText("Test Feed (rss)")).toBeTruthy();
    expect(within(c).getByText("score 0.42")).toBeTruthy();
    expect(within(c).getByText("ambiguity signal 0.00")).toBeTruthy();
    expect(within(c).getByText(/prior label: reject/)).toBeTruthy();
  });

  it("shows an unsafe original URL as text, not a link", async () => {
    await open([article("k1", 1, { url: "javascript:alert(1)" })]);
    const c = card();
    expect(within(c).getByText("javascript:alert(1)")).toBeTruthy();
    expect(within(c).queryByRole("link", { name: /javascript/ })).toBeNull();
  });

  it("posts each key's action; E posts edit_category without asking for a category", async () => {
    const keys: [string, string][] = [
      ["p", "publish"],
      ["r", "reject"],
      ["a", "ambiguous"],
      ["d", "duplicate"],
      ["s", "same_news"],
      ["u", "update"],
      ["e", "edit_category"],
      ["k", "skip"],
    ];
    const items = keys.map((_, i) => article("k" + (i + 1), i + 1));
    await open(items);
    for (let i = 0; i < keys.length; i++) {
      const [key, action] = keys[i];
      await screen.findByRole("heading", { name: "Headline k" + (i + 1) });
      fireEvent.keyDown(window, { key });
      await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.review_decide", { article_id: "k" + (i + 1), action }, false));
      expect(screen.queryByRole("dialog")).toBeNull();
    }
    expect(calls("cycle.review_decide").length).toBe(keys.length);
  });

  it("re-decides an earlier article selected in the queue", async () => {
    await open([article("k1", 1, { decision: { action: "publish", notes: "" } }), article("k2", 2)]);
    const queue = screen.getByRole("list", { name: "Queue" });
    expect(within(queue).getByText("Publish")).toBeTruthy();
    fireEvent.click(within(queue).getByText("Headline k1"));
    await screen.findByRole("heading", { name: "Headline k1" });
    fireEvent.keyDown(window, { key: "r" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("cycle.review_decide", { article_id: "k1", action: "reject" }, false));
    expect(await within(queue).findByText("Reject")).toBeTruthy();
  });

  it("opens at the first undecided article", async () => {
    await open([article("k1", 1, { decision: { action: "publish", notes: "" } }), article("k2", 2)]);
    expect(await within(card()).findByRole("heading", { name: "Headline k2" })).toBeTruthy();
  });

  it("shows a refused decision and keeps the article", async () => {
    await open([article("k1", 1)], {
      "cycle.review_decide": () => {
        throw new ApiError(409, "conflict", "step article_review is already finished");
      },
    });
    fireEvent.keyDown(window, { key: "p" });
    expect(await screen.findByText(/already finished/)).toBeTruthy();
    expect(within(card()).getByRole("heading", { name: "Headline k1" })).toBeTruthy();
  });

  it("keeps Finish disabled while articles are undecided, then finishes and calls onDone", async () => {
    const onDone = vi.fn();
    await open([article("k1", 1)], { "cycle.review_finish": () => obj({ articles_reviewed: 1, publish: 1, reject: 0 }, "review complete") }, onDone);
    const finish = screen.getByRole("button", { name: "Finish review" }) as HTMLButtonElement;
    expect(finish.disabled).toBe(true);
    expect(screen.getByText(/1 undecided/)).toBeTruthy();
    fireEvent.keyDown(window, { key: "p" });
    await waitFor(() => expect(finish.disabled).toBe(false));
    fireEvent.click(finish);
    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1));
    expect(calls("cycle.review_finish").length).toBe(1);
    expect(await screen.findByText(/1 reviewed · 1 publish · 0 reject/)).toBeTruthy();
  });

  it("pauses the review when it is left, but not after it was finished", async () => {
    const first = await open([article("k1", 1)]);
    first.unmount();
    await waitFor(() => expect(calls("cycle.review_pause").length).toBe(1));

    run.mockReset();
    const second = await open([article("k1", 1, { decision: { action: "publish", notes: "" } })], {
      "cycle.review_finish": () => obj({ articles_reviewed: 1, publish: 1, reject: 0 }, "review complete"),
    });
    fireEvent.click(screen.getByRole("button", { name: "Finish review" }));
    await waitFor(() => expect(calls("cycle.review_finish").length).toBe(1));
    await screen.findByText(/1 reviewed/);
    second.unmount();
    await new Promise((r) => setTimeout(r, 10));
    expect(calls("cycle.review_pause").length).toBe(0);
  });

  it("says the review is finished when the step is already done", async () => {
    serve([], {}, () => cycleState("article_review", "done"));
    render(<ReviewArticles />);
    expect(await screen.findByText(/The article review is finished/)).toBeTruthy();
    expect(calls("cycle.review_open").length).toBe(0);
  });
});
