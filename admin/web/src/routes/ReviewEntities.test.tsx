import { render, screen, fireEvent, waitFor, within } from "@testing-library/preact";
import { ApiError, getSession, runAction } from "../api";
import { ReviewEntities } from "./ReviewEntities";

vi.mock("../api", async (orig) => ({ ...(await orig<typeof import("../api")>()), runAction: vi.fn(), getSession: vi.fn() }));
const run = vi.mocked(runAction);

function proposal(over: Record<string, unknown> = {}) {
  return {
    id: "p1",
    name: "Ann Example",
    type: "player",
    kind: "new_entity",
    target_entity_id: "",
    confidence: 0.82,
    occurrences: 4,
    score: 3.28,
    aliases: ["A. Example"],
    verification: { position: "QB", role: "player", team: "Team One", league: "Example League" },
    affiliation: "",
    suggestion: { entity: "", team: "", basis: "", candidates: "" },
    metadata: { team: "Team One", source_hint: "roster" },
    evidence: [
      { article_id: "a1", title: "Ann Example signs", url: "https://news.example.com/ann" },
      { article_id: "a2", title: "Purged story", url: "" },
    ],
    web_sources: [{ domain: "ref.example.org", url: "https://ref.example.org/ann", title: "Ann Example profile" }],
    first_seen: "2026-11-01",
    last_seen: "2026-11-09",
    ...over,
  };
}

const duplicate = (over: Record<string, unknown> = {}) =>
  proposal({
    id: "p2",
    name: "Anne Sample",
    kind: "possible_duplicate",
    suggestion: { entity: "Anne Samples", team: "T1", basis: "fuzzy_name", candidates: "Anne Samples" },
    ...over,
  });

function queue(items: unknown[]) {
  return { result: { render: "object" as const, object: { items }, summary: `${items.length} pending entity proposal(s)` } };
}

const text = (t: string) => ({ result: { render: "text" as const, text: t } });

const catalog = {
  result: {
    render: "object" as const,
    object: { results: [{ id: "player-anne-samples", name: "Anne Samples", type: "player", team: "team-one", terms: [] }] },
  },
};

async function open(items: unknown[]) {
  run.mockResolvedValueOnce(queue(items));
  render(<ReviewEntities />);
  const first = items[0] as { name: string };
  await screen.findByRole("heading", { name: first.name });
}

const decisionCalls = () => run.mock.calls.filter(([name]) => name === "entities.approve" || name === "entities.reject");

describe("ReviewEntities", () => {
  beforeEach(() => {
    run.mockReset();
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
  });

  it("renders the card: kind, verification, suggestion, aliases, evidence and web sources", async () => {
    await open([proposal({ affiliation: "FREE AGENT" })]);
    expect(run).toHaveBeenCalledWith("entities.review_queue", { limit: 100 }, false);
    const card = screen.getByRole("region", { name: "Selected item" });
    expect(within(card).getByText("New entity")).toBeTruthy();
    expect(within(card).getByText("QB")).toBeTruthy();
    expect(within(card).getByText("Example League")).toBeTruthy();
    expect(within(card).getByText(/FREE AGENT/)).toBeTruthy();
    expect(within(card).getByText("A. Example")).toBeTruthy();
    const ev = within(card).getByRole("link", { name: "Ann Example signs" });
    expect(ev.getAttribute("href")).toBe("https://news.example.com/ann");
    expect(ev.getAttribute("rel")).toBe("noopener noreferrer");
    expect(ev.hasAttribute("data-review-open")).toBe(true);
    const web = within(card).getByRole("link", { name: "Ann Example profile" });
    expect(web.getAttribute("href")).toBe("https://ref.example.org/ann");
    expect(web.hasAttribute("data-review-open")).toBe(false);
    expect(within(card).getByText("source_hint")).toBeTruthy();
    // The queue entry: name, kind, type and score.
    const entry = screen.getByRole("list", { name: "Queue" });
    expect(entry.textContent).toContain("Ann Example");
    expect(entry.textContent).toContain("New entity");
    expect(entry.textContent).toContain("player");
    expect(entry.textContent).toContain("3.28");
  });

  it("shows a purged evidence article as text without a link", async () => {
    await open([proposal()]);
    expect(screen.getByText("Purged story")).toBeTruthy();
    expect(screen.queryByRole("link", { name: "Purged story" })).toBeNull();
    expect(screen.getByText(/no longer stored/)).toBeTruthy();
  });

  it("does not link evidence with a non-web url, and opens the first safe one", async () => {
    await open([
      proposal({
        evidence: [
          { article_id: "a0", title: "Script link", url: "javascript:alert(1)" },
          { article_id: "a1", title: "Ann Example signs", url: "https://news.example.com/ann" },
        ],
      }),
    ]);
    expect(screen.queryByRole("link", { name: "Script link" })).toBeNull();
    expect(screen.getByRole("link", { name: "Ann Example signs" }).hasAttribute("data-review-open")).toBe(true);
    expect(document.querySelectorAll("a[data-review-open]").length).toBe(1);
  });

  it("lists every evidence article, folding those after the first ten", async () => {
    const evidence = Array.from({ length: 12 }, (_, i) => ({
      article_id: "a" + i,
      title: "Story " + i,
      url: i < 11 ? "" : "https://news.example.com/s11",
    }));
    await open([proposal({ evidence })]);
    expect(screen.getByText("Story 0")).toBeTruthy();
    expect(screen.getByText("Story 9")).toBeTruthy();
    expect(screen.getByText("2 more evidence articles")).toBeTruthy();
    const folded = screen.getByText("Story 10").closest("details");
    expect(folded).toBeTruthy();
    // The only web link, though folded, is still what O opens.
    const link = screen.getByRole("link", { name: "Story 11", hidden: true });
    expect(link.hasAttribute("data-review-open")).toBe(true);
  });

  it("shows the suggestion of a possible duplicate", async () => {
    await open([duplicate()]);
    const card = screen.getByRole("region", { name: "Selected item" });
    expect(within(card).getByText("Possible duplicate")).toBeTruthy();
    expect(within(card).getAllByText(/Anne Samples/).length).toBeGreaterThan(0);
    expect(within(card).getByText(/fuzzy_name/)).toBeTruthy();
  });

  it("approves with A and shows the action text", async () => {
    await open([proposal(), proposal({ id: "p3", name: "Bo Other" })]);
    run.mockResolvedValueOnce(text("approved Ann Example (p1); run entities.generate to regenerate the deterministic entity catalog"));
    fireEvent.keyDown(window, { key: "a" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("entities.approve", { id: "p1" }, false));
    expect(await screen.findByRole("heading", { name: "Bo Other" })).toBeTruthy();
    expect(screen.getByText(/approved Ann Example \(p1\)/)).toBeTruthy();
  });

  it("rejects with R", async () => {
    await open([proposal()]);
    run.mockResolvedValueOnce(text("rejected Ann Example (p1)"));
    fireEvent.keyDown(window, { key: "r" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("entities.reject", { id: "p1" }, false));
    expect(await screen.findByText("rejected Ann Example (p1)")).toBeTruthy();
    expect(screen.getByText(/Nothing to review/)).toBeTruthy();
  });

  it("offers N and M but no Approve for a possible duplicate, and A does nothing", async () => {
    await open([duplicate()]);
    expect(screen.queryByRole("button", { name: "Approve A" })).toBeNull();
    expect(screen.getByRole("button", { name: /Approve as new/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Merge into/ })).toBeTruthy();
    expect(screen.getByRole("button", { name: /Reject/ })).toBeTruthy();
    fireEvent.keyDown(window, { key: "a" });
    await new Promise((r) => setTimeout(r, 20));
    expect(decisionCalls()).toEqual([]);
    expect(screen.getByRole("heading", { name: "Anne Sample" })).toBeTruthy();
  });

  it("offers Approve but neither N nor M for a new entity", async () => {
    await open([proposal()]);
    expect(screen.getByRole("button", { name: "Approve A" })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Approve as new/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Merge into/ })).toBeNull();
    fireEvent.keyDown(window, { key: "n" });
    fireEvent.keyDown(window, { key: "m" });
    await new Promise((r) => setTimeout(r, 20));
    expect(decisionCalls()).toEqual([]);
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("approves a possible duplicate as new with N", async () => {
    await open([duplicate()]);
    run.mockResolvedValueOnce(text("approved Anne Sample (p2)"));
    fireEvent.keyDown(window, { key: "n" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("entities.approve", { id: "p2", as_new: true }, false));
    expect(await screen.findByText("approved Anne Sample (p2)")).toBeTruthy();
  });

  it("merges a possible duplicate picked from the catalog search with M", async () => {
    await open([duplicate()]);
    run.mockResolvedValueOnce(catalog);
    fireEvent.keyDown(window, { key: "m" });
    const input = (await screen.findByRole("combobox", { name: /Search the entity catalog/ })) as HTMLInputElement;
    expect(input.value).toBe("Anne Samples");
    await screen.findByRole("option", { name: /Anne Samples/ });
    run.mockResolvedValueOnce(text("approved Anne Sample (p2)"));
    fireEvent.keyDown(input, { key: "Enter" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("entities.approve", { id: "p2", merge_into: "player-anne-samples" }, false),
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(await screen.findByText("approved Anne Sample (p2)")).toBeTruthy();
  });

  it("pre-fills the merge search with the name when nothing is suggested", async () => {
    await open([duplicate({ suggestion: { entity: "", team: "", basis: "", candidates: "" } })]);
    run.mockResolvedValue(catalog);
    fireEvent.click(screen.getByRole("button", { name: /Merge into/ }));
    const input = (await screen.findByRole("combobox", { name: /Search the entity catalog/ })) as HTMLInputElement;
    expect(input.value).toBe("Anne Sample");
  });

  it("fires no decision while typing in the merge search, and Escape returns to the review", async () => {
    await open([duplicate()]);
    run.mockResolvedValue(catalog);
    fireEvent.keyDown(window, { key: "m" });
    const input = await screen.findByRole("combobox", { name: /Search the entity catalog/ });
    for (const key of ["r", "s", "a", "n", "m", "j"]) fireEvent.keyDown(input, { key });
    fireEvent.input(input, { target: { value: "rsan" } });
    fireEvent.keyDown(document.body, { key: "r" });
    await new Promise((r) => setTimeout(r, 20));
    expect(decisionCalls()).toEqual([]);
    expect(screen.getByRole("heading", { name: "Anne Sample" })).toBeTruthy();
    fireEvent.keyDown(input, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(decisionCalls()).toEqual([]);
    // Back on the review, the shortcuts work again.
    await waitFor(() => expect((screen.getByRole("button", { name: /Reject/ }) as HTMLButtonElement).disabled).toBe(false));
    run.mockResolvedValueOnce(text("rejected Anne Sample (p2)"));
    fireEvent.keyDown(window, { key: "r" });
    await waitFor(() => expect(run).toHaveBeenCalledWith("entities.reject", { id: "p2" }, false));
  });

  it("keeps the item and shows the server message when an approval fails", async () => {
    await open([proposal()]);
    run.mockRejectedValueOnce(
      new ApiError(409, "conflict", 'entitydiscovery: "Ann Example" already exists in category "players" — reject this proposal or resolve manually'),
    );
    fireEvent.keyDown(window, { key: "a" });
    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("already exists in category");
    expect(screen.getByRole("heading", { name: "Ann Example" })).toBeTruthy();
  });

  it("removes a proposal decided elsewhere with an info notice", async () => {
    await open([proposal(), proposal({ id: "p3", name: "Bo Other" })]);
    run.mockRejectedValueOnce(new ApiError(409, "conflict", "proposal p1 is already approved"));
    fireEvent.keyDown(window, { key: "a" });
    expect(await screen.findByRole("heading", { name: "Bo Other" })).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("proposal p1 is already approved");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("removes an unknown proposal with an info notice", async () => {
    await open([proposal(), proposal({ id: "p3", name: "Bo Other" })]);
    run.mockRejectedValueOnce(new ApiError(404, "not_found", "entity proposal not found"));
    fireEvent.keyDown(window, { key: "r" });
    expect(await screen.findByRole("heading", { name: "Bo Other" })).toBeTruthy();
    expect(screen.getByRole("status").textContent).toContain("p1");
  });

  it("skips with S", async () => {
    await open([proposal(), proposal({ id: "p3", name: "Bo Other" })]);
    fireEvent.keyDown(window, { key: "s" });
    expect(await screen.findByRole("heading", { name: "Bo Other" })).toBeTruthy();
    expect(decisionCalls()).toEqual([]);
  });

  it("shows the empty queue text", async () => {
    run.mockResolvedValueOnce(queue([]));
    render(<ReviewEntities />);
    expect(await screen.findByText("Nothing to review: no entity proposal is pending.")).toBeTruthy();
  });

  it("offers a retry when the queue does not load", async () => {
    run.mockRejectedValueOnce(new ApiError(500, "internal", "internal error"));
    render(<ReviewEntities />);
    expect(await screen.findByText(/Could not load the entity review/)).toBeTruthy();
    run.mockResolvedValueOnce(queue([proposal()]));
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: "Ann Example" })).toBeTruthy();
  });
});
