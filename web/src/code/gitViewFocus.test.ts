import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GitStatus } from "./gitTypes";

const gitAPI = vi.hoisted(() => ({
  listRepositories: vi.fn(),
  loadStatus: vi.fn(),
}));

const socket = vi.hoisted(() => {
  const listeners = new Map<string, Set<(data: object) => void>>();
  return {
    on: vi.fn((type: string, callback: (data: object) => void) => {
      const callbacks = listeners.get(type) || new Set();
      callbacks.add(callback);
      listeners.set(type, callbacks);
      return () => callbacks.delete(callback);
    }),
    onState: vi.fn(() => vi.fn()),
    send: vi.fn(),
    emit(type: string, data: object) {
      listeners.get(type)?.forEach((callback) => callback(data));
    },
    clear() {
      listeners.clear();
    },
  };
});

vi.mock("./gitApi", () => gitAPI);
vi.mock("../../js/api.js", () => ({ api: vi.fn() }));
vi.mock("../../js/ws.js", () => ({
  on: socket.on,
  onState: socket.onState,
  send: socket.send,
}));

import { GitView } from "./gitView";

function status(revision: number): GitStatus {
  return {
    workspaceId: "workspace",
    repositoryId: "repository",
    revision,
    branch: "main",
    detached: false,
    ahead: 0,
    behind: 0,
    conflicts: [],
    staged: [],
    unstaged: [],
    totalChangeCount: 0,
    state: {},
  };
}

describe("Git view commit message focus", () => {
  let host: HTMLElement;
  let controller: AbortController;

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    controller = new AbortController();
    gitAPI.listRepositories.mockResolvedValue({
      repositories: [{ id: "repository", label: "Repository", parent: false, scopes: [], revision: 1 }],
      searchParentGitRepositories: false,
    });
    gitAPI.loadStatus.mockResolvedValue(status(1));
  });

  afterEach(() => {
    controller.abort();
    document.body.innerHTML = "";
    socket.clear();
    vi.clearAllMocks();
  });

  it("preserves the focused draft and caret across a live status render", async () => {
    const updateBadge = vi.fn();
    const view = new GitView(host, "workspace", controller.signal, {
      roots: () => [],
      openFile: vi.fn(),
      openDiff: vi.fn(),
      updateBadge,
    });
    await view.start();

    const input = host.querySelector<HTMLTextAreaElement>("[data-git-commit-message]")!;
    input.value = "Keep typing this commit message";
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.focus();
    input.setSelectionRange(5, 11, "forward");

    socket.emit("git_status", {
      workspaceId: "workspace",
      status: {
        ...status(2),
        unstaged: [{ path: "README.md", status: "modified", statusCode: "M", scope: "unstaged" }],
        totalChangeCount: 1,
      },
    });

    const renderedInput = host.querySelector<HTMLTextAreaElement>("[data-git-commit-message]")!;
    expect(renderedInput).not.toBe(input);
    expect(document.activeElement).toBe(renderedInput);
    expect(renderedInput.value).toBe("Keep typing this commit message");
    expect(renderedInput.selectionStart).toBe(5);
    expect(renderedInput.selectionEnd).toBe(11);
    expect(host.querySelector("[data-git-change-index='0']")).not.toBeNull();
    expect(updateBadge).toHaveBeenLastCalledWith(1);
  });

  it("does not replace the input for a revision-only status refresh", async () => {
    const view = new GitView(host, "workspace", controller.signal, {
      roots: () => [],
      openFile: vi.fn(),
      openDiff: vi.fn(),
      updateBadge: vi.fn(),
    });
    await view.start();

    const input = host.querySelector<HTMLTextAreaElement>("[data-git-commit-message]")!;
    input.focus();
    socket.emit("git_status", { workspaceId: "workspace", status: status(2) });

    expect(host.querySelector("[data-git-commit-message]")).toBe(input);
    expect(document.activeElement).toBe(input);
  });
});
