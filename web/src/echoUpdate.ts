import { get } from "../js/api.js";

export type EchoUpdateStatus = {
  updateAvailable: boolean;
  localMasterCommit: string;
  remoteMasterCommit: string;
  checkedAt: string;
};

export type EchoUpdateSnapshot = {
  status: EchoUpdateStatus | null;
  checking: boolean;
  error: string;
};

const CHECK_INTERVAL_MS = 60 * 60 * 1000;

let status: EchoUpdateStatus | null = null;
let checking = false;
let error = "";
let intervalID: number | undefined;
let inFlight: Promise<EchoUpdateSnapshot> | null = null;

export function getEchoUpdateSnapshot(): EchoUpdateSnapshot {
  return { status, checking, error };
}

/** Apply the cached update state to every currently rendered badge. */
export function syncEchoUpdateBadges(root: ParentNode = document): void {
  const available = status?.updateAvailable === true;
  root.querySelectorAll<HTMLElement>("[data-echo-update-badge]").forEach((badge) => {
    badge.hidden = !available;
  });
  root.querySelectorAll<HTMLElement>("[data-echo-update-target]").forEach((target) => {
    const baseLabel = target.dataset.echoUpdateLabel || "Settings";
    const label = available ? `${baseLabel} — Echo update available` : baseLabel;
    target.setAttribute("title", label);
    target.setAttribute("aria-label", label);
  });
}

function publish(): void {
  const snapshot = getEchoUpdateSnapshot();
  syncEchoUpdateBadges(document);
  window.dispatchEvent(new CustomEvent<EchoUpdateSnapshot>("echo:update-status", { detail: snapshot }));
}

/** Check immediately. Failures preserve the last successful update state. */
export function refreshEchoUpdateStatus(): Promise<EchoUpdateSnapshot> {
  if (inFlight) return inFlight;
  checking = true;
  publish();
  inFlight = (async () => {
    try {
      status = await get("/api/development/update-status") as EchoUpdateStatus;
      error = "";
    } catch (caught) {
      error = caught instanceof Error ? caught.message : String(caught);
    } finally {
      checking = false;
      inFlight = null;
      publish();
    }
    return getEchoUpdateSnapshot();
  })();
  return inFlight;
}

/** Start the authenticated UI's singleton startup/hourly update monitor. */
export function startEchoUpdateMonitor(): void {
  syncEchoUpdateBadges(document);
  if (intervalID !== undefined) return;
  void refreshEchoUpdateStatus();
  intervalID = window.setInterval(() => { void refreshEchoUpdateStatus(); }, CHECK_INTERVAL_MS);
}

export function stopEchoUpdateMonitor(): void {
  if (intervalID === undefined) return;
  window.clearInterval(intervalID);
  intervalID = undefined;
}
