import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ get: vi.fn() }));
vi.mock("../js/api.js", () => ({ get: api.get }));

import { installChatMap } from "./chatMap";

const response = (overrides: Record<string, unknown> = {}) => ({
  chats: [{
    workspaceId: "workspace-1", workspaceName: "Echo", chatId: "chat-1",
    surface: "chat", preview: "Implement the map", lastActivityAt: "2026-08-27T14:00:00Z",
  }],
  warnings: [],
  ...overrides,
});

function mountTrigger(mobile = false): { root: HTMLElement; trigger: HTMLButtonElement } {
  const root = document.createElement("div");
  root.innerHTML = `${mobile ? '<nav class="mobile-bottom-nav">' : ""}<button data-nav="map" aria-expanded="false">Map</button>${mobile ? "</nav>" : ""}`;
  document.body.append(root);
  return { root, trigger: root.querySelector("button")! };
}

describe("Chat Map", () => {
  beforeEach(() => {
    api.get.mockReset();
    api.get.mockResolvedValue(response());
    document.body.innerHTML = "";
  });

  afterEach(() => {
    document.body.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("renders safe cross-workspace rows and navigates to the exact selected chat", async () => {
    api.get.mockResolvedValue(response({
      chats: [{
        workspaceId: "workspace-code", workspaceName: '<script>alert("no")</script>', chatId: "code-chat",
        surface: "code", preview: '<img src=x onerror="alert(1)">', lastActivityAt: "2026-08-27T15:00:00Z",
      }],
    }));
    const { root, trigger } = mountTrigger();
    const navigate = vi.fn();
    const dispose = installChatMap(root, { navigate });

    trigger.click();
    await vi.waitFor(() => expect(document.querySelectorAll(".chat-map-item")).toHaveLength(1));
    expect(api.get).toHaveBeenCalledWith("/api/chats", expect.objectContaining({ signal: expect.any(AbortSignal) }));
    expect(document.querySelector(".chat-map-popup script")).toBeNull();
    expect(document.querySelector(".chat-map-popup img")).toBeNull();
    expect(document.querySelector(".chat-map-item")?.textContent).toContain('<img src=x onerror="alert(1)">');
    expect(document.querySelector(".chat-map-item")?.textContent).toContain('<script>alert("no")</script> · Code Chat');

    (document.querySelector(".chat-map-item") as HTMLButtonElement).click();
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({
      workspaceId: "workspace-code", chatId: "code-chat", surface: "code",
    }));
    expect(document.querySelector(".chat-map-popup")).toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    dispose();
  });

  it("supports menu keyboard navigation and restores trigger focus on Escape", async () => {
    api.get.mockResolvedValue(response({
      chats: [
        { workspaceId: "one", workspaceName: "One", chatId: "first", surface: "chat", preview: "First", lastActivityAt: "2026-08-27T15:00:00Z" },
        { workspaceId: "two", workspaceName: "Two", chatId: "second", surface: "code", preview: "Second", lastActivityAt: "2026-08-27T14:00:00Z" },
      ],
    }));
    const { root, trigger } = mountTrigger(true);
    const dispose = installChatMap(root);
    trigger.click();
    await vi.waitFor(() => expect(document.querySelectorAll(".chat-map-item")).toHaveLength(2));
    const items = [...document.querySelectorAll<HTMLButtonElement>(".chat-map-item")];
    items[0].focus();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowDown", bubbles: true }));
    expect(document.activeElement).toBe(items[1]);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(document.querySelector(".chat-map-popup")).toBeNull();
    expect(document.activeElement).toBe(trigger);
    trigger.click();
    await vi.waitFor(() => expect(document.querySelector(".chat-map-popup")).not.toBeNull());
    expect(api.get).toHaveBeenCalledTimes(2);
    dispose();
  });

  it("shows empty and partial-warning states", async () => {
    api.get.mockResolvedValue(response({
      chats: [], warnings: [{ workspaceId: "broken", workspaceName: "Broken repo", message: "Unavailable" }],
    }));
    const { root, trigger } = mountTrigger();
    const dispose = installChatMap(root);
    trigger.click();
    await vi.waitFor(() => expect(document.querySelector(".chat-map-empty")?.textContent).toBe("No chats yet."));
    expect(document.querySelector(".chat-map-warning")?.textContent).toContain("Broken repo");
    dispose();
  });

  it("offers a retry after an API failure", async () => {
    api.get.mockRejectedValueOnce(new Error("Offline")).mockResolvedValueOnce(response({ chats: [] }));
    const { root, trigger } = mountTrigger();
    const dispose = installChatMap(root);
    trigger.click();
    await vi.waitFor(() => expect(document.querySelector("[data-chat-map-retry]")).not.toBeNull());
    expect(document.querySelector(".chat-map-error")?.textContent).toContain("Offline");
    (document.querySelector("[data-chat-map-retry]") as HTMLButtonElement).click();
    await vi.waitFor(() => expect(document.querySelector(".chat-map-empty")?.textContent).toBe("No chats yet."));
    expect(api.get).toHaveBeenCalledTimes(2);
    dispose();
  });
});
