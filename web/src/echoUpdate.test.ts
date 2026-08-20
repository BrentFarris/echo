import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ get: vi.fn() }));
vi.mock("../js/api.js", () => ({ get: api.get }));

import {
  getEchoUpdateSnapshot,
  refreshEchoUpdateStatus,
  startEchoUpdateMonitor,
  stopEchoUpdateMonitor,
  syncEchoUpdateBadges,
} from "./echoUpdate";

const available = {
  updateAvailable: true,
  localMasterCommit: "1111111111111111111111111111111111111111",
  remoteMasterCommit: "2222222222222222222222222222222222222222",
  checkedAt: "2026-08-19T12:00:00Z",
};

describe("Echo update monitor", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    document.body.innerHTML = `
      <button data-echo-update-target data-echo-update-label="Settings" title="Settings" aria-label="Settings">
        <b data-echo-update-badge hidden></b>
      </button>`;
    api.get.mockReset();
  });

  afterEach(() => {
    stopEchoUpdateMonitor();
    vi.useRealTimers();
    document.body.innerHTML = "";
  });

  it("checks at startup and every hour", async () => {
    api.get.mockResolvedValue(available);
    startEchoUpdateMonitor();
    await vi.waitFor(() => expect(api.get).toHaveBeenCalledTimes(1));
    await vi.advanceTimersByTimeAsync(60 * 60 * 1000);
    expect(api.get).toHaveBeenCalledTimes(2);
  });

  it("synchronizes arrow badges and accessible labels", async () => {
    api.get.mockResolvedValue(available);
    await refreshEchoUpdateStatus();
    syncEchoUpdateBadges(document);
    expect(document.querySelector<HTMLElement>("[data-echo-update-badge]")!.hidden).toBe(false);
    expect(document.querySelector("[data-echo-update-target]")?.getAttribute("aria-label")).toContain("update available");
  });

  it("retains the last successful status when a later check fails", async () => {
    api.get.mockResolvedValueOnce(available).mockRejectedValueOnce(new Error("offline"));
    await refreshEchoUpdateStatus();
    await refreshEchoUpdateStatus();
    expect(getEchoUpdateSnapshot().status?.updateAvailable).toBe(true);
    expect(getEchoUpdateSnapshot().error).toBe("offline");
    expect(document.querySelector<HTMLElement>("[data-echo-update-badge]")!.hidden).toBe(false);
  });
});
