import { afterEach, describe, expect, it, vi } from "vitest";
import { openWorkspaceDropdown } from "../js/workspaces.js";

afterEach(() => {
  document.body.innerHTML = "";
  vi.restoreAllMocks();
});

describe("workspace dropdown", () => {
  it("opens above a bottom navigation trigger and stays inside the viewport", () => {
    vi.stubGlobal("innerWidth", 390);
    vi.stubGlobal("innerHeight", 844);
    const trigger = document.createElement("button");
    trigger.getBoundingClientRect = () => ({
      x: 8, y: 792, top: 792, right: 128, bottom: 836, left: 8,
      width: 120, height: 44, toJSON: () => ({}),
    });
    document.body.append(trigger);

    const close = openWorkspaceDropdown(trigger, {
      items: [{ id: "echo", name: "Echo", mainPath: "C:\\echo", folders: [] }],
      selectedId: "echo",
    });
    const anchor = document.querySelector<HTMLElement>(".workspace-dropdown-anchor");

    expect(anchor?.dataset.placement).toBe("above");
    expect(Number.parseFloat(anchor?.style.top || "-1")).toBeGreaterThanOrEqual(8);
    expect(trigger.getAttribute("aria-expanded")).toBe("true");
    close();
  });

  it("closes on Escape and returns focus to its trigger", () => {
    const trigger = document.createElement("button");
    trigger.getBoundingClientRect = () => ({
      x: 8, y: 8, top: 8, right: 128, bottom: 52, left: 8,
      width: 120, height: 44, toJSON: () => ({}),
    });
    document.body.append(trigger);
    openWorkspaceDropdown(trigger, { items: [], selectedId: "" });

    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));

    expect(document.querySelector(".workspace-dropdown-anchor")).toBeNull();
    expect(trigger.getAttribute("aria-expanded")).toBe("false");
    expect(document.activeElement).toBe(trigger);
  });
});
