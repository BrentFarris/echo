import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const endpoint = {
  id: "primary", name: "Primary", endpoint: "http://localhost:11434/v1", model: "test-model",
  temperature: 0.6, topK: 20, topP: 0.95, minP: 0, contextLength: 8192, maxTokens: 512,
  frequencyPenalty: 0, presencePenalty: 1.5, repetitionPenalty: 1.05,
  timeoutSeconds: 600, streamIdleTimeoutSeconds: 600, thinkingTokenBudget: -1,
  thinkingCorrection: false, headers: {},
};

const api = vi.hoisted(() => ({
  get: vi.fn(async (path: string) => {
    if (path === "/api/settings") return {
      settings: {
        endpoints: [{ ...endpoint }],
        endpointSelection: { chat: "primary", research: "primary", vision: "primary", inlineCode: "primary" },
      },
    };
    if (path === "/api/workspaces") return { workspaces: [], activeId: "" };
    if (path === "/api/auth/status") return { transportSecure: false };
    if (path === "/api/auth/sessions") return { sessions: [] };
    return {};
  }),
  post: vi.fn(),
  put: vi.fn(async (_path: string, body: { settings?: Record<string, unknown> }) => ({ settings: body.settings || {} })),
  del: vi.fn(),
}));

vi.mock("../js/api.js", () => api);
vi.mock("./auth/authGate.ts", () => ({ logout: vi.fn() }));
vi.mock("./code/persistence.ts", () => ({ hasDirtySessions: vi.fn(async () => false) }));
vi.mock("./echoUpdate.ts", () => ({
  getEchoUpdateSnapshot: () => ({ status: null, checking: false, error: "" }),
  refreshEchoUpdateStatus: vi.fn(), syncEchoUpdateBadges: vi.fn(),
}));
vi.mock("./navigation.ts", () => ({ codeRouteHash: () => "#/code", navigateBackFromSettings: vi.fn() }));
vi.mock("./primaryNav.ts", () => ({ renderMobilePrimaryNav: () => "" }));
vi.mock("../js/workspaces.js", () => ({ openAddWorkspaceModal: vi.fn(), openWorkspaceDropdown: vi.fn() }));
vi.mock("./completionNotifications.ts", () => ({
  completionNotificationPermission: () => "unsupported", requestCompletionNotificationPermission: vi.fn(),
  updateCompletionNotificationSettings: vi.fn(),
}));

import { mount, unmount } from "../js/views/settings.js";

async function flush(): Promise<void> {
  for (let index = 0; index < 8; index++) await Promise.resolve();
}

describe("context compression endpoint settings", () => {
  let root: HTMLElement;

  beforeEach(async () => {
    root = document.createElement("div");
    document.body.appendChild(root);
    api.put.mockClear();
    mount(root);
    await flush();
    root.querySelector<HTMLButtonElement>('[data-action="edit-endpoint"]')!.click();
  });

  afterEach(() => {
    unmount();
    document.body.innerHTML = "";
    vi.restoreAllMocks();
  });

  it("shows migrated defaults and persists an explicit disablement and threshold", async () => {
    const enabled = root.querySelector<HTMLInputElement>('[data-endpoint-field="contextCompressionEnabled"]')!;
    const threshold = root.querySelector<HTMLInputElement>('[data-endpoint-field="contextCompressionThresholdPercent"]')!;
    expect(enabled.checked).toBe(true);
    expect(threshold.value).toBe("70");
    expect(threshold.min).toBe("10");
    expect(threshold.max).toBe("95");

    enabled.checked = false;
    enabled.dispatchEvent(new Event("input", { bubbles: true }));
    threshold.value = "55";
    threshold.dispatchEvent(new Event("input", { bubbles: true }));
    root.querySelector<HTMLButtonElement>('[data-action="save-endpoint"]')!.click();
    await flush();

    const saved = (api.put.mock.calls.at(-1)?.[1].settings as any).endpoints[0];
    expect(saved.contextCompressionEnabled).toBe(false);
    expect(saved.contextCompressionThresholdPercent).toBe(55);
  });
});
