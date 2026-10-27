import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  put: vi.fn(),
  del: vi.fn(),
}));
vi.mock("../../js/api.js", () => api);

const ws = vi.hoisted(() => {
  const handlers = new Map<string, Set<(data: unknown) => void>>();
  return {
    on: vi.fn((type: string, handler: (data: unknown) => void) => {
      if (!handlers.has(type)) handlers.set(type, new Set());
      handlers.get(type)!.add(handler);
      return () => handlers.get(type)?.delete(handler);
    }),
    emit(type: string, data: unknown = {}) {
      handlers.get(type)?.forEach((handler) => handler(data));
    },
  };
});
vi.mock("../../js/ws.js", () => ws);

import { mount, unmount } from "./orchestrationView";

function groupFixture(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    id: "group-1",
    name: "Release work",
    workspaceIds: ["ws-1", "ws-2"],
    workspaces: [{ id: "ws-1", name: "Alpha" }, { id: "ws-2", name: "Beta" }],
    pendingTasks: [],
    ...overrides,
  };
}

function mountView(): HTMLElement {
  const host = document.createElement("div");
  document.body.append(host);
  mount(host);
  return host;
}

beforeEach(() => {
  for (const key of Object.keys(api)) (api as Record<string, ReturnType<typeof vi.fn>>)[key].mockReset();
  localStorage.clear();
});

afterEach(() => {
  unmount();
  document.body.innerHTML = "";
});

describe("orchestration page", () => {
  it("renders the empty state when no groups exist", async () => {
    api.get.mockImplementation((path: string) => {
      if (path === "/api/workspace-groups") return Promise.resolve({ groups: [] });
      if (path === "/api/workspaces") return Promise.resolve({ workspaces: [{ id: "ws-1", name: "Alpha" }] });
      if (path === "/api/settings") return Promise.resolve({ endpoints: [] });
      if (path === "/api/echo-map") return Promise.resolve({ chats: [], warnings: [] });
      throw new Error(`unexpected get ${path}`);
    });
    const host = mountView();

    const empty = await vi.waitFor(() => {
      const found = host.querySelector<HTMLElement>(".orchestration-empty");
      if (!found) throw new Error("empty state not rendered yet");
      return found;
    });
    expect(empty.textContent).toContain("Create a workspace group");
    expect(host.querySelector<HTMLSelectElement>("[data-orchestration-group-select]")?.disabled).toBe(true);
    expect(host.querySelector("[data-orchestration-composer]")?.hasAttribute("hidden")).toBe(true);
  });

  it("creates a group through the modal and selects it", async () => {
    api.get.mockImplementation((path: string) => {
      if (path === "/api/workspace-groups") {
        return Promise.resolve(api.post.mock.calls.length
          ? { groups: [groupFixture()] }
          : { groups: [] });
      }
      if (path === "/api/workspaces") return Promise.resolve({ workspaces: [{ id: "ws-1", name: "Alpha" }] });
      if (path === "/api/settings") return Promise.resolve({ endpoints: [] });
      if (path === "/api/echo-map") return Promise.resolve({ chats: [], warnings: [] });
      throw new Error(`unexpected get ${path}`);
    });
    api.post.mockResolvedValue({ group: groupFixture() });
    const host = mountView();
    await vi.waitFor(() => expect(host.querySelector(".orchestration-empty")).not.toBeNull());

    host.querySelector<HTMLButtonElement>("[data-orchestration-add]")?.click();
    const dialog = document.querySelector<HTMLDialogElement>(".orchestration-modal");
    expect(dialog).not.toBeNull();

    // Validation: no name.
    dialog!.querySelector<HTMLButtonElement>("[data-orchestration-new-create]")?.click();
    expect((dialog!.querySelector<HTMLElement>("[data-orchestration-new-error]")!).textContent).toContain("name is required");
    expect(api.post).not.toHaveBeenCalled();

    // Validation: no workspaces.
    dialog!.querySelector<HTMLInputElement>("[data-orchestration-new-name]")!.value = "My group";
    dialog!.querySelector<HTMLButtonElement>("[data-orchestration-new-create]")?.click();
    expect((dialog!.querySelector<HTMLElement>("[data-orchestration-new-error]")!).textContent).toContain("at least one workspace");
    expect(api.post).not.toHaveBeenCalled();

    dialog!.querySelector<HTMLInputElement>("[data-orchestration-new-workspaces] input")!.checked = true;
    dialog!.querySelector<HTMLButtonElement>("[data-orchestration-new-create]")?.click();
    await vi.waitFor(() => expect(api.post).toHaveBeenCalledWith("/api/workspace-groups", { name: "My group", workspaceIds: ["ws-1"] }));
    expect(document.querySelector(".orchestration-modal")).toBeNull();
    await vi.waitFor(() => expect(host.querySelector("[data-orchestration-group-select]")?.hasAttribute("disabled")).toBe(false));
  });

  it("queues a task from the composer with the selected settings", async () => {
    api.get.mockImplementation((path: string) => {
      if (path === "/api/workspace-groups") return Promise.resolve({ groups: [groupFixture()] });
      if (path === "/api/workspaces") return Promise.resolve({ workspaces: [{ id: "ws-1", name: "Alpha" }] });
      if (path === "/api/settings") return Promise.resolve({ endpoints: [{ model: "gpt-test", name: "GPT Test" }] });
      if (path === "/api/echo-map") return Promise.resolve({ chats: [], warnings: [] });
      throw new Error(`unexpected get ${path}`);
    });
    api.post.mockResolvedValue({ group: groupFixture() });
    const host = mountView();
    await vi.waitFor(() => expect(host.querySelector("[data-orchestration-composer]")?.hasAttribute("hidden")).toBe(false));

    const input = host.querySelector<HTMLDivElement>("[data-orchestration-input]")!;
    input.textContent = "Fix the flaky test";

    // Pick the model through the chat-style dropdown.
    host.querySelector<HTMLButtonElement>("[data-orchestration-model-trigger]")?.click();
    const option = [...document.querySelectorAll<HTMLButtonElement>("[data-orchestration-model-value]")]
      .find((button) => button.dataset.orchestrationModelValue === "gpt-test");
    expect(option?.textContent).toContain("GPT Test — gpt-test");
    option?.click();

    // The agent-mode dropdown lists Group Worker selected and others locked.
    host.querySelector<HTMLButtonElement>("[data-orchestration-mode-trigger]")?.click();
    const modeDropdown = document.querySelector<HTMLElement>(".mode-dropdown");
    expect(modeDropdown?.querySelector(".model-dropdown-item.is-selected")?.textContent).toContain("Group Worker");
    expect(modeDropdown?.querySelectorAll(".model-dropdown-item.is-locked[disabled]").length).toBe(4);
    const thinkingSlider = modeDropdown?.querySelector<HTMLInputElement>("[data-orchestration-thinking-slider]")!;
    thinkingSlider.value = "4";
    thinkingSlider.dispatchEvent(new Event("input", { bubbles: true }));

    host.querySelector<HTMLButtonElement>("[data-orchestration-send]")?.click();
    await vi.waitFor(() => expect(api.post).toHaveBeenCalledWith("/api/workspace-groups/group-1/tasks", {
      prompt: "Fix the flaky test", ready: true, temperature: 0.6, model: "gpt-test", reasoningEffort: "high",
    }));
    expect(input.textContent).toBe("");
  });

  it("renders pending tasks with ready checkbox, edit, and delete actions", async () => {
    api.get.mockImplementation((path: string) => {
      if (path === "/api/workspace-groups") {
        return Promise.resolve({
          groups: [groupFixture({
            pendingTasks: [
              { id: "task-1", prompt: "First task", ready: true, model: "gpt-test" },
              { id: "task-2", prompt: "Held task", ready: false },
              { id: "task-3", prompt: "Dispatched", ready: true, dispatchedWorkspaceId: "ws-2" },
            ],
          })],
        });
      }
      if (path === "/api/workspaces") return Promise.resolve({ workspaces: [] });
      if (path === "/api/settings") return Promise.resolve({ endpoints: [] });
      if (path === "/api/echo-map") return Promise.resolve({ chats: [], warnings: [] });
      throw new Error(`unexpected get ${path}`);
    });
    const host = mountView();

    await vi.waitFor(() => expect(host.querySelectorAll(".orchestration-task")).toHaveLength(3));
    const rows = [...host.querySelectorAll<HTMLElement>(".orchestration-task")];

    // Ready checkbox toggles via PUT.
    const ready = rows[0].querySelector<HTMLInputElement>(".orchestration-task-ready")!;
    expect(ready.checked).toBe(true);
    ready.click();
    await vi.waitFor(() => expect(api.put).toHaveBeenCalledWith("/api/workspace-groups/group-1/tasks/task-1", { ready: false }));

    // Held task shows the held chip.
    expect(rows[1].querySelector(".orchestration-chip-held")?.textContent).toContain("Held");

    // Dispatched task is disabled and shows the pickup chip.
    const dispatched = rows[2].querySelector<HTMLInputElement>(".orchestration-task-ready")!;
    expect(dispatched.disabled).toBe(true);
    expect(rows[2].querySelector(".orchestration-chip-dispatched")?.textContent).toContain("Beta");
    expect(rows[2].querySelector(".orchestration-task-actions")).toBeNull();

    // Edit opens the inline editor and saves through PUT.
    rows[0].querySelector<HTMLButtonElement>(".orchestration-task-action")?.click();
    await vi.waitFor(() => expect(host.querySelector(".orchestration-task-editor")).not.toBeNull());
    const editor = host.querySelector<HTMLElement>(".orchestration-task-editor")!;
    (editor.querySelector("textarea") as HTMLTextAreaElement).value = "Updated prompt";
    editor.querySelector<HTMLButtonElement>(".primary-button")?.click();
    await vi.waitFor(() => expect(api.put).toHaveBeenCalledWith("/api/workspace-groups/group-1/tasks/task-1", expect.objectContaining({ prompt: "Updated prompt" })));

    // Delete sends DELETE (after the editor unmounts on re-render).
    await vi.waitFor(() => expect(host.querySelector(".orchestration-task-editor")).toBeNull());
    host.querySelectorAll<HTMLElement>(".orchestration-task")[0]
      .querySelector<HTMLButtonElement>(".orchestration-task-delete")?.click();
    await vi.waitFor(() => expect(api.del).toHaveBeenCalledWith("/api/workspace-groups/group-1/tasks/task-1"));
  });

  it("refreshes groups when the workspace_groups_changed broadcast arrives", async () => {
    api.get.mockImplementation((path: string) => {
      if (path === "/api/workspace-groups") return Promise.resolve({ groups: [groupFixture()] });
      if (path === "/api/workspaces") return Promise.resolve({ workspaces: [] });
      if (path === "/api/settings") return Promise.resolve({ endpoints: [] });
      if (path === "/api/echo-map") return Promise.resolve({ chats: [], warnings: [] });
      throw new Error(`unexpected get ${path}`);
    });
    const host = mountView();
    await vi.waitFor(() => expect(host.querySelectorAll(".orchestration-task")).toHaveLength(0));

    ws.emit("workspace_groups_changed");
    await vi.waitFor(() => expect(api.get).toHaveBeenCalledWith("/api/workspace-groups", expect.objectContaining({ signal: expect.any(AbortSignal) })));
  });

  it("confirms before deleting a group", async () => {
    api.get.mockImplementation((path: string) => {
      if (path === "/api/workspace-groups") return Promise.resolve({ groups: [groupFixture()] });
      if (path === "/api/workspaces") return Promise.resolve({ workspaces: [] });
      if (path === "/api/settings") return Promise.resolve({ endpoints: [] });
      if (path === "/api/echo-map") return Promise.resolve({ chats: [], warnings: [] });
      throw new Error(`unexpected get ${path}`);
    });
    api.del.mockResolvedValue({ deletedId: "group-1" });
    const host = mountView();
    await vi.waitFor(() => expect(host.querySelector("[data-orchestration-delete-group]")?.hasAttribute("hidden")).toBe(false));

    host.querySelector<HTMLButtonElement>("[data-orchestration-delete-group]")?.click();
    const dialog = document.querySelector<HTMLDialogElement>(".orchestration-confirm-dialog");
    expect(dialog?.querySelector("h2")?.textContent).toContain("Release work");
    dialog!.querySelector<HTMLButtonElement>("[data-choice='confirm']")?.click();
    await vi.waitFor(() => expect(api.del).toHaveBeenCalledWith("/api/workspace-groups/group-1"));
  });
});
