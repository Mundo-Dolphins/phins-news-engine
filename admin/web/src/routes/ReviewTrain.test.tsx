import { render, screen, fireEvent, waitFor } from "@testing-library/preact";
import { ApiError, runAction } from "../api";
import { ReviewTrain } from "./ReviewTrain";

vi.mock("../api", async (orig) => ({ ...(await orig<typeof import("../api")>()), runAction: vi.fn() }));
const run = vi.mocked(runAction);

function item(over: Record<string, unknown> = {}) {
  return {
    article_id: "a1",
    proposal_id: "p1",
    score: 0.82,
    reason: "uncertain",
    nb_zone: "grey",
    nb_value: 0.51,
    llm_label: "publish",
    llm_confidence: 0.7,
    llm_reason: "The teacher thinks it is on topic.",
    suggested_label: "publish",
    purged: false,
    title: "First article",
    summary: "A full summary.",
    body: "",
    url: "https://example.com/first",
    source_name: "Example News",
    source_type: "rss",
    domain: "example.com",
    published_at: "2026-11-09T10:00:00Z",
    cluster_id: "c1",
    prior_labels: [],
    ...over,
  };
}

function queue(items: unknown[]) {
  return { result: { render: "object" as const, object: { items } } };
}

describe("ReviewTrain", () => {
  beforeEach(() => run.mockReset());

  it("loads the queue and links the original for https URLs", async () => {
    run.mockResolvedValueOnce(queue([item()]));
    render(<ReviewTrain />);
    const link = await screen.findByRole("link", { name: /Open original/ });
    expect(link.getAttribute("href")).toBe("https://example.com/first");
    expect(link.getAttribute("target")).toBe("_blank");
    expect(link.getAttribute("rel")).toBe("noopener noreferrer");
    expect(run).toHaveBeenCalledWith("train.review_queue", { limit: 50 }, false);
    expect(screen.getAllByText("A full summary.").length).toBeGreaterThan(0);
    expect(screen.getAllByText(/Example News/).length).toBeGreaterThan(0);
    expect(screen.getByText("The teacher thinks it is on topic.")).toBeTruthy();
  });

  it("offers a retry when the first load fails", async () => {
    run.mockRejectedValueOnce(new ApiError(500, "internal", "boom"));
    run.mockResolvedValueOnce(queue([item()]));
    render(<ReviewTrain />);
    expect(await screen.findByText(/Could not load the review queue: boom/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    expect(await screen.findByRole("heading", { name: "First article" })).toBeTruthy();
    expect(run).toHaveBeenCalledTimes(2);
  });

  it("renders a javascript: URL as text, not a link", async () => {
    run.mockResolvedValueOnce(queue([item({ url: "javascript:alert(1)" })]));
    render(<ReviewTrain />);
    expect(await screen.findByText("javascript:alert(1)")).toBeTruthy();
    expect(screen.queryByRole("link", { name: /Open original/ })).toBeNull();
    expect(document.querySelector('a[href^="javascript"]')).toBeNull();
  });

  it("hides the body section when empty and shows it when stored", async () => {
    run.mockResolvedValueOnce(queue([item(), item({ article_id: "a2", proposal_id: "p2", title: "Second", body: "Body text" })]));
    render(<ReviewTrain />);
    await screen.findByRole("heading", { name: "First article" });
    expect(screen.queryByText("Body")).toBeNull();
    fireEvent.keyDown(window, { key: "j" });
    expect(await screen.findByText("Body text")).toBeTruthy();
  });

  it("shows the empty state", async () => {
    run.mockResolvedValueOnce(queue([]));
    render(<ReviewTrain />);
    expect(await screen.findByText(/Nothing to review/)).toBeTruthy();
  });

  it("posts train.review_decide with the item's ids on P and removes it", async () => {
    run.mockResolvedValueOnce(queue([item(), item({ article_id: "a2", proposal_id: "p2", title: "Second" })]));
    run.mockResolvedValueOnce({
      result: { render: "object", object: { outcome: "decided", article_id: "a1" }, summary: "published a1" },
    });
    render(<ReviewTrain />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.keyDown(window, { key: "p" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("train.review_decide", { article_id: "a1", proposal_id: "p1", decision: "publish" }, false),
    );
    expect(await screen.findByRole("heading", { name: "Second" })).toBeTruthy();
    expect(screen.queryByText("First article")).toBeNull();
  });

  it("posts discard on D", async () => {
    run.mockResolvedValueOnce(queue([item()]));
    run.mockResolvedValueOnce({
      result: { render: "object", object: { outcome: "decided", article_id: "a1" }, summary: "discarded a1" },
    });
    render(<ReviewTrain />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.keyDown(window, { key: "d" });
    await waitFor(() =>
      expect(run).toHaveBeenCalledWith("train.review_decide", { article_id: "a1", proposal_id: "p1", decision: "discard" }, false),
    );
  });

  it("shows the summary of a dropped outcome and removes the item", async () => {
    run.mockResolvedValueOnce(queue([item(), item({ article_id: "a2", proposal_id: "p2", title: "Second" })]));
    run.mockResolvedValueOnce({
      result: {
        render: "object",
        object: { outcome: "dropped_already_labeled", article_id: "a1" },
        summary: "a1 was already labeled elsewhere; dropped from the queue",
      },
    });
    render(<ReviewTrain />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.keyDown(window, { key: "p" });
    expect(await screen.findByText("a1 was already labeled elsewhere; dropped from the queue")).toBeTruthy();
    expect(screen.queryByText("First article")).toBeNull();
    expect(screen.getByRole("heading", { name: "Second" })).toBeTruthy();
  });

  it("treats an entry gone from the queue as an info notice, not an error page", async () => {
    run.mockResolvedValueOnce(queue([item()]));
    run.mockRejectedValueOnce(new ApiError(404, "not_found", "a1 is not in the review queue"));
    render(<ReviewTrain />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.keyDown(window, { key: "p" });
    expect(await screen.findByText(/no longer in the review queue/)).toBeTruthy();
    expect(screen.queryByText("First article")).toBeNull();
    expect(screen.getByText(/Nothing to review/)).toBeTruthy();
  });

  it("hides the item on S without a request", async () => {
    run.mockResolvedValueOnce(queue([item(), item({ article_id: "a2", proposal_id: "p2", title: "Second" })]));
    render(<ReviewTrain />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.keyDown(window, { key: "s" });
    expect(await screen.findByRole("heading", { name: "Second" })).toBeTruthy();
    expect(screen.queryByText("First article")).toBeNull();
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("keeps skipped items hidden after a refresh", async () => {
    run.mockResolvedValue(queue([item(), item({ article_id: "a2", proposal_id: "p2", title: "Second" })]));
    render(<ReviewTrain />);
    await screen.findByRole("heading", { name: "First article" });
    fireEvent.keyDown(window, { key: "s" });
    const spy = vi.spyOn(document, "visibilityState", "get").mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    spy.mockRestore();
    await waitFor(() => expect(run).toHaveBeenCalledTimes(2));
    expect(await screen.findByRole("heading", { name: "Second" })).toBeTruthy();
    expect(screen.queryByText("First article")).toBeNull();
  });

  it("shows a purged item as a short notice", async () => {
    run.mockResolvedValueOnce(queue([item({ purged: true, title: "", summary: "", url: "", source_name: "", published_at: "" })]));
    render(<ReviewTrain />);
    expect(await screen.findByText(/no longer exists/)).toBeTruthy();
    expect(screen.queryByRole("link", { name: /Open original/ })).toBeNull();
  });

  it("shows prior labels", async () => {
    run.mockResolvedValueOnce(
      queue([item({ prior_labels: [{ label: "discard", labeled_by: "ana", labeled_at: "2026-11-01T09:00:00Z", source: "cli:train-review" }] })]),
    );
    render(<ReviewTrain />);
    expect(await screen.findByText(/cli:train-review/)).toBeTruthy();
  });
});
