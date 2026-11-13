import { act, render, screen } from "@testing-library/preact";
import { getSession, listActions, type CatalogItem } from "../api";
import { Review } from "./Review";

vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  getSession: vi.fn(),
  listActions: vi.fn(),
  runAction: vi.fn(),
}));

const item = (name: string) => ({ name }) as CatalogItem;

describe("Review index", () => {
  beforeEach(() => vi.mocked(listActions).mockResolvedValue([item("events.rules_queue")]));

  it("links the train review in local mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    render(<Review />);
    expect(await screen.findByRole("link", { name: /Train review/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /Dataset review/ }).getAttribute("href")).toBe("/review/dataset");
    expect(screen.getByRole("link", { name: /Entity review/ }).getAttribute("href")).toBe("/review/entities");
    expect(screen.getByRole("link", { name: /Event rule review/ }).getAttribute("href")).toBe("/review/events");
  });

  it("links the editorial cycle wizard in local mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    render(<Review />);
    expect((await screen.findByRole("link", { name: /Editorial cycle/ })).getAttribute("href")).toBe("/cycle");
  });

  it("hides the dictionary update review when the host does not register it", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    // The catalog answers only when the test says so, after the index rendered.
    let answer: ((items: CatalogItem[]) => void) | undefined;
    vi.mocked(listActions).mockReturnValue(new Promise((resolve) => (answer = resolve)));
    render(<Review />);
    await screen.findByRole("link", { name: /Train review/ });
    expect(answer).toBeDefined();
    await act(async () => answer!([item("events.rules_queue")]));
    expect(screen.getByRole("link", { name: /Event rule review/ })).toBeTruthy();
    expect(screen.queryByRole("link", { name: /Dictionary update review/ })).toBeNull();
  });

  it("links the dictionary update review when the host registers it", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    vi.mocked(listActions).mockResolvedValue([item("dictionary.update_session")]);
    render(<Review />);
    const link = await screen.findByRole("link", { name: /Dictionary update review/ });
    expect(link.getAttribute("href")).toBe("/review/dictionary");
  });

  it("says the screens are local-only in production mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "production" });
    render(<Review />);
    expect(await screen.findByText("Review screens are only available in local mode.")).toBeTruthy();
    expect(screen.queryByRole("link", { name: /Train review/ })).toBeNull();
  });
});
