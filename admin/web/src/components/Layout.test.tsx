import { render, screen, fireEvent } from "@testing-library/preact";
import { getHealth, getSession, getLLMStatus, LLM_CHANGED } from "../api";
import { Layout } from "./Layout";

vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  getHealth: vi.fn(),
  getSession: vi.fn(),
  getLLMStatus: vi.fn(),
}));

describe("Layout", () => {
  beforeEach(() => {
    vi.mocked(getHealth).mockResolvedValue({ status: "ok", mode: "local", schema: "ok" });
    vi.mocked(getLLMStatus).mockResolvedValue({ servers: [], selection: null });
  });

  it("shows the Review nav in local mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    render(<Layout>x</Layout>);
    expect(await screen.findByRole("link", { name: "Review" })).toBeTruthy();
  });

  it("links the editorial cycle in local mode only", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    render(<Layout>x</Layout>);
    expect((await screen.findByRole("link", { name: "Editorial cycle" })).getAttribute("href")).toBe("/cycle");
  });

  it("hides the Review nav in production mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "production" });
    render(<Layout>x</Layout>);
    await screen.findByText(/Log out \(ana\)/);
    expect(screen.queryByRole("link", { name: "Review" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Editorial cycle" })).toBeNull();
  });

  it("shows the active LLM selection in the nav after LLM_CHANGED", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    render(<Layout>x</Layout>);
    expect((await screen.findByRole("link", { name: "LLM" })).getAttribute("href")).toBe("/settings/llm");
    vi.mocked(getLLMStatus).mockResolvedValue({
      servers: [],
      selection: { server: "server-a", model: "m1" },
    });
    fireEvent(window, new Event(LLM_CHANGED));
    expect(await screen.findByRole("link", { name: "LLM (server-a · m1)" })).toBeTruthy();
  });

  it("hides the LLM link in production mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "production" });
    render(<Layout>x</Layout>);
    await screen.findByText(/Log out \(ana\)/);
    expect(screen.queryByRole("link", { name: /^LLM/ })).toBeNull();
  });
});
