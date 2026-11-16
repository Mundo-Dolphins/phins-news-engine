import { render, screen, fireEvent, waitFor } from "@testing-library/preact";
import { LLMSettings } from "./LLMSettings";

const servers = [
  { name: "server-a", base_url: "http://a/v1" },
  { name: "server-b", base_url: "http://b/v1" },
];

type Status = { servers: typeof servers; selection: { server: string; model: string } | null };

let status: Status;
let modelsFor: Record<string, string[] | string>;
let gates: Record<string, Promise<void>>;
let calls: { name: string; params: Record<string, unknown> }[];

function ok(object: unknown) {
  return new Response(JSON.stringify({ result: { render: "object", object } }), { status: 200 });
}

beforeEach(() => {
  status = { servers, selection: null };
  modelsFor = { "server-a": ["m1", "m2"] };
  calls = [];
  gates = {};
  vi.stubGlobal(
    "fetch",
    vi.fn(async (path: string, init?: RequestInit) => {
      if (path === "/api/session") return new Response(JSON.stringify({ username: "ana", mode: "local" }));
      const name = decodeURIComponent(path.replace("/api/actions/", ""));
      const params = JSON.parse(String(init?.body)).params as Record<string, unknown>;
      calls.push({ name, params });
      if (name === "llm.status") return ok(status);
      if (name === "llm.models") {
        await gates[params.server as string];
        const m = modelsFor[params.server as string];
        if (typeof m === "string" || m === undefined) {
          return new Response(JSON.stringify({ error: { code: "internal", message: m ?? "unknown server" } }), {
            status: 502,
          });
        }
        return ok({ server: params.server, models: m });
      }
      if (name === "llm.select") {
        status = { servers, selection: { server: params.server as string, model: params.model as string } };
        return ok(status);
      }
      if (name === "llm.clear") {
        status = { servers, selection: null };
        return ok(status);
      }
      return new Response("{}", { status: 404 });
    }),
  );
});

afterEach(() => vi.unstubAllGlobals());

describe("LLMSettings", () => {
  it("shows the settings.yml defaults line", async () => {
    render(<LLMSettings />);
    expect(await screen.findByText("Using settings.yml defaults")).toBeTruthy();
    expect(screen.queryByRole("button", { name: "Reset to settings.yml" })).toBeNull();
  });

  it("loads the models of the picked server", async () => {
    render(<LLMSettings />);
    fireEvent.change(await screen.findByLabelText("Server"), { target: { value: "server-a" } });
    expect(await screen.findByLabelText("Model")).toBeTruthy();
    expect(screen.getByRole("option", { name: "m2" })).toBeTruthy();
  });

  it("drops a slower response for a server that is no longer picked", async () => {
    let releaseA!: () => void;
    gates["server-a"] = new Promise<void>((r) => (releaseA = r));
    modelsFor["server-b"] = ["b1"];
    render(<LLMSettings />);
    const server = await screen.findByLabelText("Server");
    fireEvent.change(server, { target: { value: "server-a" } });
    fireEvent.change(server, { target: { value: "server-b" } });
    expect(await screen.findByRole("option", { name: "b1" })).toBeTruthy();
    releaseA();
    await waitFor(() => expect(calls.filter((c) => c.name === "llm.models")).toHaveLength(2));
    await new Promise((r) => setTimeout(r, 10));
    expect(screen.queryByRole("option", { name: "m1" })).toBeNull();
    expect(screen.getByRole("option", { name: "b1" })).toBeTruthy();
  });

  it("shows the message and no model dropdown for an unavailable server", async () => {
    modelsFor["server-b"] = "connection refused";
    render(<LLMSettings />);
    fireEvent.change(await screen.findByLabelText("Server"), { target: { value: "server-b" } });
    expect(await screen.findByText("Server unavailable: connection refused")).toBeTruthy();
    expect(screen.queryByLabelText("Model")).toBeNull();
  });

  it("saves the selection and shows it", async () => {
    render(<LLMSettings />);
    fireEvent.change(await screen.findByLabelText("Server"), { target: { value: "server-a" } });
    const save = screen.getByRole("button", { name: "Save" }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);
    fireEvent.change(await screen.findByLabelText("Model"), { target: { value: "m1" } });
    expect(save.disabled).toBe(false);
    fireEvent.click(save);
    expect(await screen.findByText("Using server-a · m1")).toBeTruthy();
    expect(calls.find((c) => c.name === "llm.select")?.params).toEqual({ server: "server-a", model: "m1" });
  });

  it("resets to settings.yml", async () => {
    status = { servers, selection: { server: "server-a", model: "m1" } };
    render(<LLMSettings />);
    expect(await screen.findByText("Using server-a · m1")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Reset to settings.yml" }));
    expect(await screen.findByText("Using settings.yml defaults")).toBeTruthy();
    await waitFor(() => expect(calls.some((c) => c.name === "llm.clear")).toBe(true));
  });

  it("shows the no-servers error", async () => {
    (fetch as ReturnType<typeof vi.fn>).mockImplementation(async (path: string) =>
      path === "/api/session"
        ? new Response(JSON.stringify({ username: "ana", mode: "local" }))
        : new Response(JSON.stringify({ error: { code: "bad_request", message: "no llm_servers configured" } }), {
            status: 400,
          }),
    );
    render(<LLMSettings />);
    expect(await screen.findByText(/no llm_servers configured/)).toBeTruthy();
  });
});
