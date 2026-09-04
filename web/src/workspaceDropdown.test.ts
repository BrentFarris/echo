import { afterEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({
  get: vi.fn(), post: vi.fn(), put: vi.fn(), del: vi.fn(),
}));
vi.mock("../js/api.js", () => api);

import { openAddWorkspaceModal, openEditWorkspaceModal, openWorkspaceDropdown } from "../js/workspaces.js";

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

describe("add workspace modal", () => {
  it("preserves the main folder value when another folder is added", () => {
    openAddWorkspaceModal();
    const mainFolder = document.querySelector<HTMLInputElement>('[data-folder-path="0"]');
    expect(mainFolder).not.toBeNull();
    mainFolder!.value = "C:\\projects\\echo";

    document.querySelector<HTMLButtonElement>('[data-action="add-folder"]')!.click();

    expect(document.querySelector<HTMLInputElement>('[data-folder-path="0"]')).toBe(mainFolder);
    expect(mainFolder!.value).toBe("C:\\projects\\echo");
    expect(document.querySelectorAll("[data-folder-row]")).toHaveLength(2);
    expect(document.activeElement).toBe(document.querySelector('[data-folder-path="1"]'));
  });
});

describe("edit workspace modal", () => {
  const workspace = {
    id: "echo", name: "Echo", mainPath: "C:\\projects\\echo", iconExt: "png",
    folders: ["C:\\projects\\echo", "C:\\projects\\shared"],
  };

  it("prepopulates editable fields and keeps the main folder read-only", () => {
    openEditWorkspaceModal(workspace);

    expect(document.querySelector<HTMLInputElement>('[data-field="name"]')?.value).toBe("Echo");
    const main = document.querySelector<HTMLInputElement>('[data-folder-path="0"]')!;
    expect(main.value).toBe("C:\\projects\\echo");
    expect(main.readOnly).toBe(true);
    expect(document.querySelector<HTMLInputElement>('[data-folder-path="1"]')?.value).toBe("C:\\projects\\shared");
    expect(document.querySelector('[data-icon-preview] img')?.getAttribute("src")).toContain("/api/workspaces/echo/icon");
  });

  it("submits name, additional folders, and explicit icon removal", async () => {
    api.put.mockResolvedValueOnce({ workspace: { ...workspace, name: "Echo Renamed", iconExt: "" } });
    openEditWorkspaceModal(workspace);
    document.querySelector<HTMLInputElement>('[data-field="name"]')!.value = "Echo Renamed";
    document.querySelector<HTMLButtonElement>('[data-action="remove-icon"]')!.click();
    document.querySelector<HTMLButtonElement>('[data-action="save-workspace"]')!.click();

    await vi.waitFor(() => expect(api.put).toHaveBeenCalledWith("/api/workspaces/echo", {
      name: "Echo Renamed", folders: ["C:\\projects\\shared"], removeIcon: true,
    }));
    expect(document.querySelector("[data-workspace-modal-backdrop]")).toBeNull();
  });

  it("submits replacement image bytes and extension", async () => {
    const originalCreateObjectURL = URL.createObjectURL;
    const originalRevokeObjectURL = URL.revokeObjectURL;
    Object.defineProperty(URL, "createObjectURL", { configurable: true, value: vi.fn(() => "blob:workspace-icon") });
    Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: vi.fn() });
    try {
      api.put.mockResolvedValueOnce({ workspace: { ...workspace, iconExt: "webp" } });
      openEditWorkspaceModal(workspace);
      const input = document.querySelector<HTMLInputElement>('[data-icon-input]')!;
      Object.defineProperty(input, "files", {
        configurable: true,
        value: [new File(["new-image"], "replacement.webp", { type: "image/webp" })],
      });
      input.dispatchEvent(new Event("change", { bubbles: true }));
      await vi.waitFor(() => expect(document.querySelector('[data-icon-name]')?.textContent).toBe("replacement.webp"));
      document.querySelector<HTMLButtonElement>('[data-action="save-workspace"]')!.click();

      await vi.waitFor(() => expect(api.put).toHaveBeenCalledWith("/api/workspaces/echo", {
        name: "Echo", folders: ["C:\\projects\\shared"],
        icon: { data: "bmV3LWltYWdl", ext: "webp" },
      }));
    } finally {
      if (originalCreateObjectURL) Object.defineProperty(URL, "createObjectURL", { configurable: true, value: originalCreateObjectURL });
      else delete (URL as { createObjectURL?: typeof URL.createObjectURL }).createObjectURL;
      if (originalRevokeObjectURL) Object.defineProperty(URL, "revokeObjectURL", { configurable: true, value: originalRevokeObjectURL });
      else delete (URL as { revokeObjectURL?: typeof URL.revokeObjectURL }).revokeObjectURL;
    }
  });

  it("keeps the editor open and re-enables Save after an API error", async () => {
    api.put.mockRejectedValueOnce(new Error("Folder is unavailable"));
    openEditWorkspaceModal(workspace);
    document.querySelector<HTMLButtonElement>('[data-action="save-workspace"]')!.click();

    await vi.waitFor(() => expect(document.querySelector('[data-modal-error]')?.textContent).toContain("Folder is unavailable"));
    expect(document.querySelector<HTMLButtonElement>('[data-action="save-workspace"]')!.disabled).toBe(false);
    expect(document.querySelector("[data-workspace-modal-backdrop]")).not.toBeNull();
  });
});
