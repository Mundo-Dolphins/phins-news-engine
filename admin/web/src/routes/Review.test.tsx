import { render, screen } from "@testing-library/preact";
import { getSession } from "../api";
import { Review } from "./Review";

vi.mock("../api", async (orig) => ({ ...(await orig<typeof import("../api")>()), getSession: vi.fn(), runAction: vi.fn() }));

describe("Review index", () => {
  it("links the train review in local mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    render(<Review />);
    expect(await screen.findByRole("link", { name: /Train review/ })).toBeTruthy();
    expect(screen.getByRole("link", { name: /Dataset review/ }).getAttribute("href")).toBe("/review/dataset");
  });

  it("says the screens are local-only in production mode", async () => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "production" });
    render(<Review />);
    expect(await screen.findByText("Review screens are only available in local mode.")).toBeTruthy();
    expect(screen.queryByRole("link", { name: /Train review/ })).toBeNull();
  });
});
