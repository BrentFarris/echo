import { on as onSocket, onState as onSocketState, send as sendSocket } from "../js/ws.js";
import * as sourceControlAPI from "./code/sourceControlApi";
import type { SourceControlRepository, SourceControlStatus } from "./code/sourceControlTypes";
import { normalizeStatus } from "./code/sourceControlTypes";
import { aggregateSourceControlChangeCount } from "./code/sourceControlIdentity";

/** Keep the desktop and mobile Source Control badges in sync. */
export function setSourceControlBadgeCount(root: ParentNode, count: number): void {
  const normalized = Number.isFinite(count) ? Math.max(0, Math.floor(count)) : 0;
  root.querySelectorAll<HTMLElement>("[data-source-control-badge], [data-git-badge]").forEach((badge) => {
    badge.hidden = normalized === 0;
    badge.textContent = normalized === 0 ? "" : normalized > 99 ? "99+" : String(normalized);
  });
}

class SourceControlBadgeMonitor {
  private readonly root: ParentNode;
  private readonly workspaceId: string;
  private readonly repositories = new Map<string, SourceControlRepository>();
  private readonly statuses = new Map<string, SourceControlStatus>();
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
    sendSocket({ type: "source_control_unsubscribe", workspaceId: this.workspaceId });
  }

  private installSocket(): void {
    let opened = false;
    this.unsubscribe.push(onSocketState((state: string) => {
      if (state !== "open" || this.disposed) return;
      sendSocket({ type: "source_control_subscribe", workspaceId: this.workspaceId });
      if (opened) void this.reloadRepositories();
      opened = true;
    }));
    this.unsubscribe.push(onSocket("source_control_status", (data: object) => {
      const event = data as { workspaceId?: string; status?: SourceControlStatus };
      if (event.workspaceId !== this.workspaceId || !event.status) return;
      this.acceptStatus(normalizeStatus(event.status as Parameters<typeof normalizeStatus>[0]));
    }));
    this.unsubscribe.push(onSocket("git_status", (data: object) => {
      const event = data as { workspaceId?: string; status?: SourceControlStatus };
      if (event.workspaceId !== this.workspaceId || !event.status) return;
      this.acceptStatus(normalizeStatus(event.status as Parameters<typeof normalizeStatus>[0]));
    }));
    this.unsubscribe.push(onSocket("source_control_resync_required", (data: object) => {
      const event = data as { workspaceId?: string };
      if (event.workspaceId === this.workspaceId) void this.reloadRepositories();
    }));
    sendSocket({ type: "source_control_subscribe", workspaceId: this.workspaceId });
  }

  private async reloadRepositories(): Promise<void> {
    const generation = ++this.reloadGeneration;
    try {
      const response = await sourceControlAPI.listRepositories(this.workspaceId);
      if (this.disposed || generation !== this.reloadGeneration) return;
      const repositories = new Map((response.repositories || []).map((repository) => [repository.id, repository]));
      this.repositories.clear();
      repositories.forEach((repository, repositoryId) => this.repositories.set(repositoryId, repository));
      for (const repositoryId of this.statuses.keys()) {
        if (!repositories.has(repositoryId)) this.statuses.delete(repositoryId);
      }
      this.render();
      await Promise.allSettled([...repositories.values()].filter((repository) => repository.available).map(async (repository) => {
        const status = await sourceControlAPI.loadStatus(this.workspaceId, repository.id);
        if (!this.disposed && generation === this.reloadGeneration) this.acceptStatus(status);
      }));
    } catch (error) {
      if (!this.disposed && generation === this.reloadGeneration) {
        console.error("Failed to load Source Control badge:", error);
      }
    }
  }

  private acceptStatus(status: SourceControlStatus): void {
    if (this.disposed || !this.repositories.has(status.repositoryId)) return;
    const current = this.statuses.get(status.repositoryId);
    if (current && status.revision < current.revision) return;
    this.statuses.set(status.repositoryId, status);
    this.render();
  }

  private render(): void {
    setSourceControlBadgeCount(this.root, aggregateSourceControlChangeCount(this.repositories.values(), this.statuses));
  }
}

/** Start tracking provider-neutral Source Control changes for one workspace. */
export function watchSourceControlBadge(root: ParentNode, workspaceId: string): () => void {
  setSourceControlBadgeCount(root, 0);
  if (!workspaceId) return () => undefined;
  const monitor = new SourceControlBadgeMonitor(root, workspaceId);
  monitor.start();
  return () => monitor.dispose();
}

// Compatibility aliases for callers using the pre-provider API.
export const setGitBadgeCount = setSourceControlBadgeCount;
export function watchGitBadge(root: ParentNode, workspaceId: string): () => void {
  const stop = watchSourceControlBadge(root, workspaceId);
  return () => {
    stop();
    sendSocket({ type: "git_unsubscribe", workspaceId });
  };
}
