import type { FileRef } from "./types";
import { refKey } from "./types";

export type TabDescriptor = {
  id: string;
  ref: FileRef | null;
  title: string;
  pinned: boolean;
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
    const previewIndex = this.tabs.findIndex((candidate) => !candidate.pinned && !candidate.dirty);
    if (previewIndex >= 0) {
      const replacedId = this.tabs[previewIndex].id;
      this.tabs.splice(previewIndex, 1, { ...tab, pinned: false });
      this.activeId = tab.id;
      return { replacedId };
    }
    this.tabs.push({ ...tab, pinned: false });
    this.activeId = tab.id;
    return {};
  }

  openPinned(tab: TabDescriptor): void {
    const tabRef = tab.ref;
    const existing = tabRef && this.tabs.find((candidate) => candidate.ref && refKey(candidate.ref) === refKey(tabRef));
    if (existing) {
      existing.pinned = true;
      this.activeId = existing.id;
      return;
    }
    this.tabs.push({ ...tab, pinned: true });
    this.activeId = tab.id;
  }

  markDirty(id: string): void {
    const tab = this.tabs.find((candidate) => candidate.id === id);
    if (!tab) return;
    tab.dirty = true;
    tab.pinned = true;
  }

  close(id: string): void {
    const index = this.tabs.findIndex((candidate) => candidate.id === id);
    if (index < 0) return;
    this.tabs.splice(index, 1);
    if (this.activeId === id) {
      this.activeId = this.tabs[Math.min(index, this.tabs.length - 1)]?.id || null;
    }
  }
}
