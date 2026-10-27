import type { FileRef, PersistedTab } from "./types";
import { refKey } from "./types";

export type TabOpenState = { keepOpen: boolean; pinned: boolean };

export function restoreTabOpenState(persisted: Pick<PersistedTab, "pinned" | "closeProtected">): TabOpenState {
  const pinned = persisted.closeProtected === true;
  return { keepOpen: persisted.pinned || pinned, pinned };
}

export function setTabPinned(tab: TabOpenState, pinned: boolean): void {
  tab.pinned = pinned;
  if (pinned) tab.keepOpen = true;
}

export type TabDescriptor = TabOpenState & {
  id: string;
  ref: FileRef | null;
  title: string;
  dirty: boolean;
};

export class TabState {
  readonly tabs: TabDescriptor[] = [];
  activeId: string | null = null;

  openPreview(tab: TabDescriptor): { replacedId?: string } {
    const tabRef = tab.ref;
    const existing = tabRef && this.tabs.find((candidate) => candidate.ref && refKey(candidate.ref) === refKey(tabRef));
    if (existing) {
      this.activeId = existing.id;
      return {};
    }
    const previewIndex = this.tabs.findIndex((candidate) => !candidate.keepOpen && !candidate.pinned && !candidate.dirty);
    if (previewIndex >= 0) {
      const replacedId = this.tabs[previewIndex].id;
      this.tabs.splice(previewIndex, 1, { ...tab, keepOpen: false, pinned: false });
      this.activeId = tab.id;
      return { replacedId };
    }
    this.tabs.push({ ...tab, keepOpen: false, pinned: false });
    this.activeId = tab.id;
    return {};
  }

  openKeptOpen(tab: TabDescriptor): void {
    const tabRef = tab.ref;
    const existing = tabRef && this.tabs.find((candidate) => candidate.ref && refKey(candidate.ref) === refKey(tabRef));
    if (existing) {
      existing.keepOpen = true;
      this.activeId = existing.id;
      return;
    }
    this.tabs.push({ ...tab, keepOpen: true, pinned: false });
    this.activeId = tab.id;
  }

  markDirty(id: string): void {
    const tab = this.tabs.find((candidate) => candidate.id === id);
    if (!tab) return;
    tab.dirty = true;
    tab.keepOpen = true;
  }

  close(id: string): void {
    const index = this.tabs.findIndex((candidate) => candidate.id === id);
    if (index < 0 || this.tabs[index].pinned) return;
    this.tabs.splice(index, 1);
    if (this.activeId === id) {
      this.activeId = this.tabs[Math.min(index, this.tabs.length - 1)]?.id || null;
    }
  }
}
