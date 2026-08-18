import { on as onSocket, onState as onSocketState, send as sendSocket } from "../js/ws.js";
import * as gitAPI from "./code/gitApi";
import type { GitStatus } from "./code/gitTypes";

/** Keep the desktop and mobile Source Control badges in sync. */
export function setGitBadgeCount(root: ParentNode, count: number): void {
  const normalized = Number.isFinite(count) ? Math.max(0, Math.floor(count)) : 0;
  root.querySelectorAll<HTMLElement>("[data-git-badge]").forEach((badge) => {
    badge.hidden = normalized === 0;
    badge.textContent = normalized === 0 ? "" : normalized > 99 ? "99+" : String(normalized);
  });
}

class GitBadgeMonitor {
  private readonly root: ParentNode;
  private readonly workspaceId: string;
  private readonly repositories = new Set<string>();
  private readonly statuses = new Map<string, GitStatus>();
  private readonly unsubscribe: Array<() => void> = [];
  private reloadGeneration = 0;
  private disposed = false;

  constructor(root: ParentNode, workspaceId: string) {
    this.root = root;
    this.workspaceId = workspaceId;
    this.installSocket();
  }

  start(): void {
    void this.reloadRepositories();
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    this.reloadGeneration += 1;
    this.unsubscribe.splice(0).forEach((unsubscribe) => unsubscribe());
    sendSocket({ type: "git_unsubscribe", workspaceId: this.workspaceId });
  }

  private installSocket(): void {
    let opened = false;
    this.unsubscribe.push(onSocketState((state: string) => {
      if (state !== "open" || this.disposed) return;
      sendSocket({ type: "git_subscribe", workspaceId: this.workspaceId });
      if (opened) void this.reloadRepositories();
      opened = true;
    }));
    this.unsubscribe.push(onSocket("git_status", (data: object) => {
      const event = data as { workspaceId?: string; status?: GitStatus };
      if (event.workspaceId !== this.workspaceId || !event.status) return;
      this.acceptStatus(event.status);
    }));
    this.unsubscribe.push(onSocket("git_resync_required", (data: object) => {
      const event = data as { workspaceId?: string };
      if (event.workspaceId === this.workspaceId) void this.reloadRepositories();
    }));
    sendSocket({ type: "git_subscribe", workspaceId: this.workspaceId });
  }

  private async reloadRepositories(): Promise<void> {
    const generation = ++this.reloadGeneration;
    try {
      const response = await gitAPI.listRepositories(this.workspaceId);
      if (this.disposed || generation !== this.reloadGeneration) return;
      const repositoryIds = new Set((response.repositories || []).map((repository) => repository.id));
      this.repositories.clear();
      repositoryIds.forEach((repositoryId) => this.repositories.add(repositoryId));
      for (const repositoryId of this.statuses.keys()) {
        if (!repositoryIds.has(repositoryId)) this.statuses.delete(repositoryId);
      }
      this.render();
      await Promise.allSettled([...repositoryIds].map(async (repositoryId) => {
        const status = await gitAPI.loadStatus(this.workspaceId, repositoryId);
        if (!this.disposed && generation === this.reloadGeneration) this.acceptStatus(status);
      }));
    } catch (error) {
      if (!this.disposed && generation === this.reloadGeneration) {
        console.error("Failed to load Git status badge:", error);
      }
    }
  }

  private acceptStatus(status: GitStatus): void {
    if (this.disposed || !this.repositories.has(status.repositoryId)) return;
    const current = this.statuses.get(status.repositoryId);
    if (current && status.revision < current.revision) return;
    this.statuses.set(status.repositoryId, status);
    this.render();
  }

  private render(): void {
    const total = [...this.statuses.values()].reduce((sum, status) => sum + status.totalChangeCount, 0);
    setGitBadgeCount(this.root, total);
  }
}

/**
 * Start tracking the total Git changes for one workspace. The returned cleanup
 * must be called before tracking a different workspace or unmounting the view.
 */
export function watchGitBadge(root: ParentNode, workspaceId: string): () => void {
  setGitBadgeCount(root, 0);
  if (!workspaceId) return () => undefined;
  const monitor = new GitBadgeMonitor(root, workspaceId);
  monitor.start();
  return () => monitor.dispose();
}
