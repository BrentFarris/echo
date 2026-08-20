import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  get: vi.fn(async (path: string) => {
    if (path === "/api/settings") return { settings: { endpoints: [] } };
    if (path === "/api/workspaces") return { workspaces: [], activeId: "" };
    if (path === "/api/auth/status") return { transportSecure: false };
    if (path === "/api/auth/sessions") return { sessions: [] };
    return {};
  }),
  post: vi.fn(),
  put: vi.fn(async (_path: string, body: { settings?: Record<string, unknown> }) => ({ settings: body.settings || {} })),
  del: vi.fn(),
}));

const notifications = vi.hoisted(() => ({
  permission: "default" as "default" | "granted" | "denied" | "unsupported",
  request: vi.fn(async () => "granted"),
  update: vi.fn(),
}));

vi.mock("../js/api.js", () => api);
vi.mock("./completionNotifications.ts", () => ({
  completionNotificationPermission: () => notifications.permission,
  requestCompletionNotificationPermission: notifications.request,
  updateCompletionNotificationSettings: notifications.update,
}));
vi.mock("./auth/authGate.ts", () => ({ logout: vi.fn() }));
vi.mock("./code/persistence.ts", () => ({ hasDirtySessions: vi.fn(async () => false) }));
vi.mock("./echoUpdate.ts", () => ({
  getEchoUpdateSnapshot: () => ({ status: null, checking: false, error: "" }),
  refreshEchoUpdateStatus: vi.fn(), syncEchoUpdateBadges: vi.fn(),
}));
vi.mock("./navigation.ts", () => ({ codeRouteHash: () => "#/code", navigateBackFromSettings: vi.fn() }));
vi.mock("./primaryNav.ts", () => ({ renderMobilePrimaryNav: () => "" }));
vi.mock("../js/workspaces.js", () => ({ openAddWorkspaceModal: vi.fn(), openWorkspaceDropdown: vi.fn() }));

import { mount, unmount } from "../js/views/settings.js";

describe("completion notification settings", () => {
  let root: HTMLElement;

  beforeEach(async () => {
    root = document.createElement("div");
    document.body.appendChild(root);
    notifications.permission = "default";
    notifications.request.mockClear();
    notifications.update.mockClear();
    api.put.mockClear();
    mount(root);
    await Promise.resolve();
    await Promise.resolve();
    root.querySelector<HTMLButtonElement>("[data-section=messaging]")!.click();
    await Promise.resolve();
    api.put.mockClear();
  });

  afterEach(() => {
    unmount();
    document.body.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("defaults both controls on and persists explicit opt-outs", async () => {
    const sounds = root.querySelector<HTMLInputElement>("[data-notification-setting=notificationSounds]")!;
    const chat = root.querySelector<HTMLInputElement>("[data-notification-setting=chatCompletionNotifications]")!;
    expect(sounds.checked).toBe(true);
    expect(chat.checked).toBe(true);

    sounds.checked = false;
    sounds.dispatchEvent(new Event("change", { bubbles: true }));
    await Promise.resolve();
    expect(api.put.mock.calls.at(-1)?.[1].settings).toEqual(expect.objectContaining({
      disableNotificationSounds: true,
      enableChatCompletionNotifications: true,
    }));

    const rerenderedChat = root.querySelector<HTMLInputElement>("[data-notification-setting=chatCompletionNotifications]")!;
    rerenderedChat.checked = false;
    rerenderedChat.dispatchEvent(new Event("change", { bubbles: true }));
    await Promise.resolve();
    expect(api.put.mock.calls.at(-1)?.[1].settings).toEqual(expect.objectContaining({
      disableNotificationSounds: true,
      enableChatCompletionNotifications: false,
    }));
    expect(notifications.update).toHaveBeenCalled();
  });

  it("offers a user-initiated browser permission action", async () => {
    root.querySelector<HTMLButtonElement>("[data-action=enable-browser-notifications]")!.click();
    await Promise.resolve();
    expect(notifications.request).toHaveBeenCalledOnce();
  });
});
