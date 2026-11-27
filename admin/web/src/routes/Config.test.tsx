import { render, screen } from "@testing-library/preact";
import { getSession, listActions, listConfigFiles, readConfig, type CatalogItem } from "../api";
import { Config } from "./Config";

vi.mock("../api", async (orig) => ({
  ...(await orig<typeof import("../api")>()),
  runAction: vi.fn(),
  getSession: vi.fn(),
  listActions: vi.fn(),
  listConfigFiles: vi.fn(),
  readConfig: vi.fn(),
  applyConfig: vi.fn(),
}));

const catalogItem = (name: string): CatalogItem => ({
  name,
  group: "config",
  title: name,
  description: "",
  kind: "read",
  modes: ["local"],
  long_running: false,
  exclusive: false,
  schema: { type: "object", properties: {} },
});

const configCatalog = () => ["config.files", "config.read", "config.apply"].map(catalogItem);

describe("Config", () => {
  beforeEach(() => {
    vi.mocked(getSession).mockResolvedValue({ username: "ana", mode: "local" });
    vi.mocked(listActions).mockResolvedValue(configCatalog());
    vi.mocked(listConfigFiles).mockResolvedValue([
      { key: "settings", title: "settings.yml", path: "configs/settings.yml", configured: true, exists: true },
    ]);
    vi.mocked(readConfig).mockResolvedValue({
      key: "settings",
      title: "settings.yml",
      path: "configs/settings.yml",
      value: { output: { destination: "telegram" } },
      comments: {},
      sections: ["output"],
    });
  });

  it("says it is not available when the host registers no config actions", async () => {
    vi.mocked(listActions).mockResolvedValue([catalogItem("events.rules_queue")]);
    render(<Config />);
    expect(await screen.findByText("Not available on this host")).toBeTruthy();
    expect(vi.mocked(listConfigFiles)).not.toHaveBeenCalled();
  });

  it("shows the file selector when the host registers the config actions", async () => {
    render(<Config />);
    expect(await screen.findByRole("heading", { name: "Configuration" })).toBeTruthy();
    expect(await screen.findByText("settings.yml")).toBeTruthy();
  });
});
