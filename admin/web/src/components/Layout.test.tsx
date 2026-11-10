import { render, screen } from "@testing-library/preact";
import { getHealth, getSession } from "../api";
import { Layout } from "./Layout";

vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  getHealth: vi.fn(),
  getSession: vi.fn(),
}));

describe("Layout", () => {
  beforeEach(() => {
    vi.mocked(getHealth).mockResolvedValue({ status: "ok", mode: "local", schema: "ok" });
  });

  it("shows the Review nav in local mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    render(<Layout>x</Layout>);
    expect(await screen.findByRole("link", { name: "Review" })).toBeTruthy();
  });

  it("hides the Review nav in production mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "production" });
    render(<Layout>x</Layout>);
    await screen.findByText(/Log out \(ana\)/);
    expect(screen.queryByRole("link", { name: "Review" })).toBeNull();
  });
});
