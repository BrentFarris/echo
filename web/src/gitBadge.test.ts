import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { GitStatus } from "./code/gitTypes";

const gitAPI = vi.hoisted(() => ({
  listRepositories: vi.fn(),
  loadStatus: vi.fn(),
}));

const socket = vi.hoisted(() => {
  const handlers = new Map<string, (data: object) => void>();
  let stateHandler: ((state: string) => void) | undefined;
  return {
    handlers,
    on: vi.fn((type: string, handler: (data: object) => void) => {
      handlers.set(type, handler);
      return () => handlers.delete(type);
    }),
    onState: vi.fn((handler: (state: string) => void) => {
      stateHandler = handler;
      handler("open");
      return () => { stateHandler = undefined; };
    }),
    send: vi.fn(),
    emit(type: string, data: object) { handlers.get(type)?.(data); },
  };
});

vi.mock("./code/gitApi", () => gitAPI);
vi.mock("../js/ws.js", () => ({ on: socket.on, onState: socket.onState, send: socket.send }));

import { setGitBadgeCount, watchGitBadge } from "./gitBadge";

function status(repositoryId: string, totalChangeCount: number, revision = 1): GitStatus {
  return {
    workspaceId: "workspace-one",
    repositoryId,
    revision,
    branch: "main",
    detached: false,
    ahead: 0,
    behind: 0,
    conflicts: [],
    staged: [],
    unstaged: [],
    totalChangeCount,
    state: {},
  };
}

describe("Git activity badge", () => {
  let root: HTMLElement;

  beforeEach(() => {
    root = document.createElement("div");
    root.innerHTML = "<b data-git-badge></b><b data-git-badge></b>";
    gitAPI.listRepositories.mockResolvedValue({
      repositories: [
        { id: "repo-one", label: "One", parent: false, scopes: [], revision: 1 },
        { id: "repo-two", label: "Two", parent: false, scopes: [], revision: 1 },
      ],
      searchParentGitRepositories: false,
    });
    gitAPI.loadStatus.mockImplementation(async (_workspaceId: string, repositoryId: string) => (
      repositoryId === "repo-one" ? status(repositoryId, 2) : status(repositoryId, 3)
    ));
  });

  afterEach(() => {
    vi.clearAllMocks();
    socket.handlers.clear();
  });

  it("hides zero changes and shows the same bounded count on every navigation badge", () => {
    setGitBadgeCount(root, 0);
    expect([...root.querySelectorAll<HTMLElement>("[data-git-badge]")].every((badge) => badge.hidden)).toBe(true);
    expect(root.textContent).toBe("");

    setGitBadgeCount(root, 128);
    expect([...root.querySelectorAll<HTMLElement>("[data-git-badge]")].every((badge) => !badge.hidden)).toBe(true);
    expect([...root.querySelectorAll("[data-git-badge]")].map((badge) => badge.textContent)).toEqual(["99+", "99+"]);
  });

  it("loads all repositories and updates the total from live Git status events", async () => {
    const stop = watchGitBadge(root, "workspace-one");

    await vi.waitFor(() => expect(root.querySelector("[data-git-badge]")?.textContent).toBe("5"));
    expect(gitAPI.loadStatus).toHaveBeenCalledTimes(2);

    socket.emit("git_status", {
      workspaceId: "workspace-one",
      status: status("repo-one", 7, 2),
    });
    expect(root.querySelector("[data-git-badge]")?.textContent).toBe("10");

    socket.emit("git_status", {
      workspaceId: "another-workspace",
      status: status("repo-one", 20, 3),
    });
    expect(root.querySelector("[data-git-badge]")?.textContent).toBe("10");

    stop();
    expect(socket.send).toHaveBeenLastCalledWith({ type: "git_unsubscribe", workspaceId: "workspace-one" });
  });

  it("has an explicit CSS hidden state that wins over the badge display rule", () => {
    const css = readFileSync(resolve(process.cwd(), "src/code/code.css"), "utf8");
    expect(css).toMatch(/\.code-git-activity\s*>\s*b\[hidden\]\s*\{\s*display:\s*none;/);
  });
});
