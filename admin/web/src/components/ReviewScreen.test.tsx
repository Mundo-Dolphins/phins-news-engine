import { render, screen, fireEvent, waitFor } from "@testing-library/preact";
import { ReviewScreen, type Notice } from "./ReviewScreen";

interface It {
  id: string;
  title: string;
}

const items: It[] = [
  { id: "a", title: "Alpha" },
  { id: "b", title: "Bravo" },
  { id: "c", title: "Charlie" },
];

function setup(over: Partial<Parameters<typeof ReviewScreen<It>>[0]> = {}) {
  const onDecide = vi.fn(async (_t: It): Promise<Notice | void> => undefined);
  const onSkip = vi.fn();
  const onRefresh = vi.fn();
  const utils = render(
    <ReviewScreen<It>
      items={items}
      itemKey={(t) => t.id}
      renderItem={(t) => <h2>Item {t.title}</h2>}
      renderListEntry={(t) => <span>{t.title}</span>}
      decisions={[{ key: "p", label: "Publish", onDecide }]}
      onSkip={onSkip}
      onRefresh={onRefresh}
      emptyText="Nothing to review."
      {...over}
    />,
  );
  return { ...utils, onDecide, onSkip, onRefresh };
}

describe("ReviewScreen", () => {
  it("shows the empty text when there are no items", () => {
    setup({ items: [] });
    expect(screen.getByText("Nothing to review.")).toBeTruthy();
    expect(screen.queryByRole("button", { name: /Publish/ })).toBeNull();
  });

  it("moves the selection with J/K and the arrow keys", () => {
    setup();
    expect(screen.getByRole("heading", { name: "Item Alpha" })).toBeTruthy();
    fireEvent.keyDown(window, { key: "j" });
    expect(screen.getByRole("heading", { name: "Item Bravo" })).toBeTruthy();
    fireEvent.keyDown(window, { key: "ArrowDown" });
    expect(screen.getByRole("heading", { name: "Item Charlie" })).toBeTruthy();
    fireEvent.keyDown(window, { key: "j" });
    expect(screen.getByRole("heading", { name: "Item Charlie" })).toBeTruthy();
    fireEvent.keyDown(window, { key: "K" });
    expect(screen.getByRole("heading", { name: "Item Bravo" })).toBeTruthy();
    fireEvent.keyDown(window, { key: "ArrowUp" });
    expect(screen.getByRole("heading", { name: "Item Alpha" })).toBeTruthy();
  });

  it("selects an item clicked in the queue", () => {
    setup();
    fireEvent.click(screen.getByText("Charlie"));
    expect(screen.getByRole("heading", { name: "Item Charlie" })).toBeTruthy();
  });

  it("calls onDecide for the selected item on the decision key", async () => {
    const { onDecide } = setup();
    fireEvent.keyDown(window, { key: "j" });
    fireEvent.keyDown(window, { key: "P" });
    await waitFor(() => expect(onDecide).toHaveBeenCalledWith(items[1]));
  });

  it("calls onDecide from the decision button", async () => {
    const { onDecide } = setup();
    fireEvent.click(screen.getByRole("button", { name: /Publish/ }));
    await waitFor(() => expect(onDecide).toHaveBeenCalledWith(items[0]));
  });

  it("shows the notice returned by a decision", async () => {
    setup({
      decisions: [{ key: "p", label: "Publish", onDecide: async () => ({ kind: "info", text: "dropped a" }) }],
    });
    fireEvent.keyDown(window, { key: "p" });
    expect(await screen.findByText("dropped a")).toBeTruthy();
  });

  it("sends one decision when the key is pressed twice before it settles", async () => {
    let release = () => {};
    const onDecide = vi.fn(() => new Promise<void>((r) => (release = r)));
    setup({ decisions: [{ key: "p", label: "Publish", onDecide }] });
    fireEvent.keyDown(window, { key: "p" });
    fireEvent.keyDown(window, { key: "p" });
    expect(onDecide).toHaveBeenCalledTimes(1);
    release();
    await waitFor(() => expect(screen.getByRole("button", { name: /Publish/ }).hasAttribute("disabled")).toBe(false));
  });

  it("ignores auto-repeated decision and skip keys but still moves on repeat", async () => {
    const { onDecide, onSkip } = setup();
    fireEvent.keyDown(window, { key: "p", repeat: true });
    fireEvent.keyDown(window, { key: "s", repeat: true });
    expect(onDecide).not.toHaveBeenCalled();
    expect(onSkip).not.toHaveBeenCalled();
    fireEvent.keyDown(window, { key: "j", repeat: true });
    expect(screen.getByRole("heading", { name: "Item Bravo" })).toBeTruthy();
  });

  it("calls onSkip on S", () => {
    const { onSkip } = setup();
    fireEvent.keyDown(window, { key: "s" });
    expect(onSkip).toHaveBeenCalledWith(items[0]);
  });

  it("ignores shortcuts while an input has focus", () => {
    const { onDecide, onSkip } = setup({ renderItem: (t) => <input aria-label={"note " + t.id} /> });
    const input = screen.getByLabelText("note a");
    input.focus();
    fireEvent.keyDown(input, { key: "p" });
    fireEvent.keyDown(input, { key: "s" });
    fireEvent.keyDown(input, { key: "j" });
    expect(onDecide).not.toHaveBeenCalled();
    expect(onSkip).not.toHaveBeenCalled();
    expect(screen.getByLabelText("note a")).toBeTruthy();
  });

  it("ignores shortcuts with Ctrl, Meta or Alt held", () => {
    const { onDecide, onSkip } = setup();
    fireEvent.keyDown(window, { key: "p", ctrlKey: true });
    fireEvent.keyDown(window, { key: "p", metaKey: true });
    fireEvent.keyDown(window, { key: "s", altKey: true });
    fireEvent.keyDown(window, { key: "j", metaKey: true });
    expect(onDecide).not.toHaveBeenCalled();
    expect(onSkip).not.toHaveBeenCalled();
    expect(screen.getByRole("heading", { name: "Item Alpha" })).toBeTruthy();
  });

  it("opens the item's marked original link on O", () => {
    const clicked = vi.fn((e: Event) => e.preventDefault());
    setup({
      renderItem: (t) => (
        <a href={"https://example.com/" + t.id} data-review-open onClick={clicked}>
          original
        </a>
      ),
    });
    fireEvent.keyDown(window, { key: "o" });
    expect(clicked).toHaveBeenCalledTimes(1);
  });

  it("refreshes when the tab becomes visible", () => {
    const { onRefresh } = setup();
    const spy = vi.spyOn(document, "visibilityState", "get").mockReturnValue("hidden");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(onRefresh).not.toHaveBeenCalled();
    spy.mockReturnValue("visible");
    document.dispatchEvent(new Event("visibilitychange"));
    expect(onRefresh).toHaveBeenCalledTimes(1);
    spy.mockRestore();
  });

  it("refreshes once when the window regains focus, even alongside visibilitychange", () => {
    const { onRefresh } = setup();
    window.dispatchEvent(new Event("focus"));
    expect(onRefresh).toHaveBeenCalledTimes(1);
    // The same tab switch also fires visibilitychange: no second load.
    document.dispatchEvent(new Event("visibilitychange"));
    expect(onRefresh).toHaveBeenCalledTimes(1);
  });

  it("keeps the position when the selected item leaves the queue", () => {
    const { rerender, onDecide, onSkip, onRefresh } = setup();
    fireEvent.keyDown(window, { key: "j" });
    rerender(
      <ReviewScreen<It>
        items={[items[0], items[2]]}
        itemKey={(t) => t.id}
        renderItem={(t) => <h2>Item {t.title}</h2>}
        renderListEntry={(t) => <span>{t.title}</span>}
        decisions={[{ key: "p", label: "Publish", onDecide }]}
        onSkip={onSkip}
        onRefresh={onRefresh}
        emptyText="Nothing to review."
      />,
    );
    expect(screen.getByRole("heading", { name: "Item Charlie" })).toBeTruthy();
  });

  it("offers a decision only for the items it applies to", async () => {
    const only = vi.fn(async (_t: It): Promise<Notice | void> => undefined);
    const always = vi.fn(async (_t: It): Promise<Notice | void> => undefined);
    setup({
      decisions: [
        { key: "a", label: "Approve", offered: (t) => t.id !== "a", onDecide: only },
        { key: "r", label: "Reject", onDecide: always },
      ],
    });
    expect(screen.queryByRole("button", { name: /Approve/ })).toBeNull();
    expect(screen.getByRole("button", { name: /Reject/ }).className).toContain("primary");
    fireEvent.keyDown(window, { key: "a" });
    await Promise.resolve();
    expect(only).not.toHaveBeenCalled();
    fireEvent.keyDown(window, { key: "j" });
    expect(screen.getByRole("button", { name: /Approve/ }).className).toContain("primary");
    fireEvent.keyDown(window, { key: "a" });
    await waitFor(() => expect(only).toHaveBeenCalledWith(items[1]));
  });
});
