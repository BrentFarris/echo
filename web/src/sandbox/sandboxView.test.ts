import { beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn() }));
const wsHandlers = vi.hoisted(() => new Map<string, (value: any) => void>());
const workspace = vi.hoisted(() => ({ id: "workspace-one", name: "Echo", iconExt: "" }));
const rfbInstances = vi.hoisted(() => [] as any[]);

vi.mock("../../js/api.js", () => api);
vi.mock("../../js/workspaces.js", () => ({
  loadWorkspaces: vi.fn(async () => [workspace]), getActive: vi.fn(() => workspace),
  setActiveWorkspace: vi.fn(), renderWorkspaceIcon: vi.fn(() => "E"),
  openWorkspaceDropdown: vi.fn(() => vi.fn()), openAddWorkspaceModal: vi.fn(() => vi.fn()),
}));
vi.mock("../../js/ws.js", () => ({
  send: vi.fn(() => true),
  on: vi.fn((type: string, handler: (value: any) => void) => { wsHandlers.set(type, handler); return () => wsHandlers.delete(type); }),
  onState: vi.fn((handler: (state: string) => void) => { handler("open"); return vi.fn(); }),
}));
vi.mock("@novnc/novnc", () => ({ default: class FakeRFB extends EventTarget {
  viewOnly = true; scaleViewport = false; clipViewport = false; resizeSession = false; qualityLevel = 0; compressionLevel = 0;
  disconnect = vi.fn(); focus = vi.fn();
  constructor(public target: HTMLElement, public url: string, public options: any) { super(); rfbInstances.push(this); }
} }));

import { mount, unmount } from "./sandboxView";

const hostStatus = { available: true, supported: true, linuxEngine: true, architecture: "amd64", serverVersion: "28.0", images: { workbench: { present: true }, desktop: { present: true }, gateway: { present: true } } };
const readyStatus = { state: "ready", enabled: true, protocolVersion: "1", controlOwner: "none", activeViewers: 0, resources: { memoryBytes: 1024, memoryLimitBytes: 4096, activeProcesses: 3 }, setup: { state: "succeeded" } };
const config = { enabled: true, cpuLimit: 4, memoryMiB: 6144, idleTimeoutMinutes: 30 };

function responses(): void {
  api.get.mockImplementation(async (path: string) => {
    if (path === "/api/sandbox/host") return hostStatus;
    if (path.endsWith("/network-grants")) return { grants: [] };
    return { config, status: readyStatus };
  });
  api.post.mockImplementation(async (path: string, body: any) => {
    if (path.endsWith("/desktop-sessions")) return { session: { id: "desktop-one", credential: "memory-only-secret" } };
    if (path.endsWith("/desktop-control")) return { lease: { owner: body.action === "take" ? "user" : "none", revision: 2 } };
    return { status: readyStatus };
  });
  api.put.mockResolvedValue({ config, status: readyStatus });
  api.del.mockResolvedValue({ deleted: true });
}

async function settle(): Promise<void> { await new Promise((resolve) => setTimeout(resolve, 0)); await new Promise((resolve) => setTimeout(resolve, 0)); }

beforeEach(() => {
  unmount(); document.body.innerHTML = ""; localStorage.clear(); vi.clearAllMocks(); wsHandlers.clear(); rfbInstances.length = 0; responses();
  vi.spyOn(window, "confirm").mockReturnValue(true);
});

describe("sandbox view", () => {
  it("opens Code with the Run and Debug sidebar selected", async () => {
    const root = document.createElement("div"); document.body.append(root); mount(root); await settle();

    root.querySelector<HTMLButtonElement>("[data-nav=debug]")?.click();

    expect(location.hash).toBe("#/code?sidebar=debug");
  });

  it.each(["disabled", "unavailable", "pulling", "creating", "starting", "ready", "stopping", "stopped", "error"])(
    "renders the %s lifecycle state",
    async (state) => {
      api.get.mockImplementation(async (path: string) => {
        if (path === "/api/sandbox/host") return hostStatus;
        if (path.endsWith("/network-grants")) return { grants: [] };
        return { config: { ...config, enabled: state !== "disabled" }, status: { ...readyStatus, state, enabled: state !== "disabled" } };
      });
      const root = document.createElement("div"); document.body.append(root); mount(root); await settle();
      expect(root.querySelector("[data-sandbox-state]")?.textContent).toBe(state);
      expect(root.querySelector("[data-sandbox-state]")?.getAttribute("data-state")).toBe(state);
    },
  );

  it("warns before first sign-in and only opens the VNC session after acknowledgment", async () => {
    const root = document.createElement("div"); document.body.append(root); mount(root); await settle();
    expect(root.querySelector<HTMLElement>("[data-signin-warning]")?.hidden).toBe(false);
    expect(api.post).not.toHaveBeenCalledWith(expect.stringContaining("desktop-sessions"), expect.anything());
    root.querySelector<HTMLButtonElement>("[data-action=acknowledge-signin]")?.click(); await settle();
    expect(rfbInstances).toHaveLength(1);
    expect(rfbInstances[0].viewOnly).toBe(true);
    expect(rfbInstances[0].options.credentials.password).toBe("memory-only-secret");
  });

  it("transitions between AI/view-only and user control without reconnecting", async () => {
    localStorage.setItem("echo:sandbox-signin-warning:v1:workspace-one", "acknowledged");
    const root = document.createElement("div"); document.body.append(root); mount(root); await settle();
    expect(rfbInstances).toHaveLength(1);
    wsHandlers.get("sandbox_event")?.({
      type: "sandbox_event", workspaceId: "workspace-one", event: "desktop_lease",
      status: { ...readyStatus, controlOwner: "ai", desktopLease: { owner: "ai", revision: 1, chatTurnId: "turn-one" } },
    });
    expect(root.querySelector(".sandbox-lease")?.textContent).toContain("AI is controlling");
    root.querySelector<HTMLButtonElement>("[data-action=take-control]")?.click(); await settle();
    expect(rfbInstances[0].viewOnly).toBe(false);
    expect(root.querySelector("[data-action=return-control]")).not.toBeNull();
		wsHandlers.get("sandbox_event")?.({
			type: "sandbox_event", workspaceId: "workspace-one", event: "desktop_lease",
			status: { ...readyStatus, controlOwner: "user", desktopLease: { owner: "user", revision: 3 } },
		});
		expect(rfbInstances[0].viewOnly).toBe(true);
		expect(root.querySelector("[data-action=take-control]")).not.toBeNull();

		root.querySelector<HTMLButtonElement>("[data-action=take-control]")?.click(); await settle();
    root.querySelector<HTMLButtonElement>("[data-action=return-control]")?.click(); await settle();
    expect(rfbInstances[0].viewOnly).toBe(true);
    expect(root.querySelector("[data-action=take-control]")).not.toBeNull();

    wsHandlers.get("sandbox_event")?.({ type: "sandbox_event", workspaceId: "workspace-one", event: "status", status: { ...readyStatus, state: "stopping", controlOwner: "none" } });
    expect(root.querySelector("[data-sandbox-state]")?.textContent).toBe("stopping");
  });

	it("reconnects with a fresh one-use desktop session after an unexpected disconnect", async () => {
		localStorage.setItem("echo:sandbox-signin-warning:v1:workspace-one", "acknowledged");
		const root = document.createElement("div"); document.body.append(root); mount(root); await settle();
		expect(rfbInstances).toHaveLength(1);
		vi.useFakeTimers();
		try {
			rfbInstances[0].dispatchEvent(new Event("disconnect"));
			await vi.advanceTimersByTimeAsync(1000);
			await Promise.resolve();
			expect(rfbInstances).toHaveLength(2);
			expect(api.post).toHaveBeenCalledTimes(2);
		} finally {
			vi.useRealTimers();
		}
	});

  it("requires destructive confirmation before browser reset", async () => {
    localStorage.setItem("echo:sandbox-signin-warning:v1:workspace-one", "acknowledged");
    const root = document.createElement("div"); document.body.append(root); mount(root); await settle();
    vi.mocked(window.confirm).mockReturnValue(false);
    root.querySelector<HTMLButtonElement>("[data-action=reset-browser]")?.click(); await settle();
    expect(api.post).not.toHaveBeenCalledWith(expect.stringContaining("/actions"), expect.objectContaining({ action: "reset_browser" }));
    vi.mocked(window.confirm).mockReturnValue(true);
    root.querySelector<HTMLButtonElement>("[data-action=reset-browser]")?.click(); await settle();
    expect(api.post).toHaveBeenCalledWith(expect.stringContaining("/actions"), { action: "reset_browser" });
    expect(localStorage.getItem("echo:sandbox-signin-warning:v1:workspace-one")).toBeNull();
  });
});
