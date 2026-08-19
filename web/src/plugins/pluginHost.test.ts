import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";

const apiState = vi.hoisted(() => ({ workspaceId: "workspace-1" }));
const api = vi.hoisted(() => ({
  get: vi.fn(async (path: string) => path === "/api/workspaces"
    ? { activeId: apiState.workspaceId }
    : { safeMode: false, plugins: [{
      id: "example-plugin", name: "Example Plugin", version: "1.0.0", digest: "digest-1",
      source: { type: "local", path: "C:/plugins/example" }, globalEnabled: true,
      workspaceEnabled: false, effective: true, compatible: true, permissions: [],
      views: [
        { pluginId: "example-plugin", id: "dashboard", kind: "page", title: "Dashboard" },
        { pluginId: "example-plugin", id: "calculator", kind: "floating", title: "Calculator", defaultSize: { width: 340, height: 420 } },
      ],
    }], stages: [] }),
  post: vi.fn(async (path: string) => path.endsWith("/sessions")
    ? { session: {
      id: `ui-session-${apiState.workspaceId}`, bridgeToken: apiState.workspaceId === "workspace-1" ? "bridge-token" : "bridge-token-2", nonce: apiState.workspaceId === "workspace-1" ? "nonce-1" : "nonce-2",
      pluginId: "example-plugin", pluginName: "Example Plugin", viewId: path.includes("calculator") ? "calculator" : "dashboard",
      viewTitle: "Plugin view", viewKind: path.includes("calculator") ? "floating" : "page",
      workspaceId: apiState.workspaceId, digest: "digest-1", entryUrl: `/plugin-ui/${apiState.workspaceId === "workspace-1" ? "bridge-token" : "bridge-token-2"}/ui/index.html`,
      expiresAt: new Date(Date.now() + 60_000).toISOString(), config: {},
    } }
    : { result: { ok: true } }),
}));

vi.mock("../../js/api.js", () => api);
vi.mock("../../js/ws.js", () => ({ on: vi.fn(() => () => undefined) }));

import { initializePluginHost, mountPluginPage, resetPluginHost } from "./pluginHost";
import { renderMobilePrimaryNav, renderPrimaryNav } from "../primaryNav";

describe("plugin host", () => {
  beforeAll(() => {
    vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true })));
    document.body.innerHTML = `${renderPrimaryNav({ active: "chat", workspaceName: "Test" })}${renderMobilePrimaryNav({ active: "chat", workspaceName: "Test" })}`;
  });

  afterAll(() => {
    resetPluginHost();
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
  });

  it("keeps core navigation fixed, isolates frames, and rejects forged bridge messages", async () => {
    await initializePluginHost();

    expect([...document.querySelectorAll("[data-nav]")].map(node => node.getAttribute("data-nav")))
      .toEqual(["workspace", "chat", "code", "git", "settings", "workspace", "chat", "code", "git", "settings"]);
    expect(document.querySelectorAll("[data-plugin-nav-section] [data-plugin-id]")).toHaveLength(2);
    expect(document.querySelector("[data-plugin-overflow]")).not.toBeNull();

    const pageRoot = document.createElement("div");
    document.body.append(pageRoot);
    const mounted = await mountPluginPage(pageRoot, "example-plugin", "dashboard");
    const iframe = pageRoot.querySelector<HTMLIFrameElement>("iframe")!;
    expect(iframe.getAttribute("sandbox")).toBe("allow-scripts");
    expect(iframe.hasAttribute("allow")).toBe(false);
    expect(iframe.getAttribute("referrerpolicy")).toBe("no-referrer");

	const themeMessages = vi.spyOn(iframe.contentWindow!, "postMessage");
	window.dispatchEvent(new CustomEvent("echo:theme-changed"));
	expect(themeMessages).toHaveBeenCalledWith(expect.objectContaining({
		type: "echo-plugin-theme", nonce: "nonce-1", pluginId: "example-plugin", viewId: "dashboard",
	}), "*");

    api.post.mockClear();
    window.dispatchEvent(new MessageEvent("message", {
      source: iframe.contentWindow,
      data: { type: "echo-plugin-request", nonce: "forged", pluginId: "example-plugin", viewId: "dashboard", id: "bad", method: "storage.get", params: { key: "x" } },
    }));
    await Promise.resolve();
    expect(api.post).not.toHaveBeenCalled();

    window.dispatchEvent(new MessageEvent("message", {
      source: iframe.contentWindow,
      data: { type: "echo-plugin-request", nonce: "nonce-1", pluginId: "different-plugin", viewId: "dashboard", id: "bad-2", method: "storage.get", params: { key: "x" } },
    }));
    await Promise.resolve();
    expect(api.post).not.toHaveBeenCalled();

    window.dispatchEvent(new MessageEvent("message", {
      source: iframe.contentWindow,
      data: { type: "echo-plugin-request", nonce: "nonce-1", pluginId: "example-plugin", viewId: "dashboard", id: "good", method: "storage.get", params: { scope: "workspace", key: "x" } },
    }));
    await Promise.resolve();
    expect(api.post).toHaveBeenCalledWith("/api/plugins/ui-sessions/bridge-token/bridge", {
      nonce: "nonce-1", method: "storage.get", params: { scope: "workspace", key: "x" },
    });

    apiState.workspaceId = "workspace-2";
    window.dispatchEvent(new CustomEvent("echo:workspace-changed"));
    await vi.waitFor(() => expect(iframe.getAttribute("src")).toContain("bridge-token-2"));

    mounted.unmount();
    pageRoot.remove();
  });
});
