import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const fixture = vi.hoisted(() => ({ settings: {} as Record<string, unknown> }));
const api = vi.hoisted(() => ({
  get: vi.fn(async (path: string) => {
    if (path === "/api/settings") return { settings: fixture.settings };
    if (path === "/api/workspaces") return { workspaces: [], activeId: "" };
    if (path === "/api/auth/status") return { transportSecure: false };
    if (path === "/api/auth/sessions") return { sessions: [] };
    return {};
  }),
  put: vi.fn(async (_path: string, body: { settings: Record<string, unknown> }) => {
    fixture.settings = body.settings;
    return { settings: fixture.settings };
  }),
  post: vi.fn(), del: vi.fn(),
}));
vi.mock("../js/api.js", () => api);
vi.mock("./auth/authGate.ts", () => ({ logout: vi.fn() }));
vi.mock("./code/persistence.ts", () => ({ hasDirtySessions: vi.fn(async () => false) }));
vi.mock("./echoUpdate.ts", () => ({
  getEchoUpdateSnapshot: () => ({ status: null, checking: false, error: "" }),
  refreshEchoUpdateStatus: vi.fn(), syncEchoUpdateBadges: vi.fn(),
}));
vi.mock("./navigation.ts", () => ({ codeRouteHash: () => "#/code", navigateBackFromSettings: vi.fn() }));
vi.mock("./primaryNav.ts", () => ({ renderMobilePrimaryNav: () => "" }));
vi.mock("../js/workspaces.js", () => ({ openAddWorkspaceModal: vi.fn(), openWorkspaceDropdown: vi.fn() }));

import { mount, unmount } from "../js/views/settings.js";

describe("Code settings", () => {
  let root: HTMLElement;
  const field = (selector: string) => root.querySelector<HTMLInputElement>(selector)!;
  async function change(selector: string, value: string | boolean) {
    const element = field(selector);
    if (typeof value === "boolean") element.checked = value;
    else element.value = value;
    element.dispatchEvent(new Event("change", { bubbles: true }));
    await Promise.resolve();
    await Promise.resolve();
  }
  beforeEach(async () => {
    fixture.settings = { endpoints: [], editorFontSize: 17, editorInsertSpaces: true, editorTabSize: 2, headers: { retained: "yes" } };
    api.put.mockClear();
    location.hash = "#/settings?section=code";
    root = document.createElement("div");
    document.body.append(root);
    mount(root);
    await vi.waitFor(() => expect(field("[data-editor-font-size]").disabled).toBe(false));
  });
  afterEach(() => { unmount(); document.body.replaceChildren(); location.hash = ""; });

  it("groups the existing font and indentation defaults under Code", async () => {
    const sections = [...root.querySelectorAll<HTMLElement>("[data-section]")].map((node) => node.dataset.section);
    expect(sections.indexOf("code") + 1).toBe(sections.indexOf("source-control"));
    expect(field("[data-editor-font-size]").value).toBe("17");
    expect(field("[data-editor-indent-style]").value).toBe("spaces");
    expect(field("[data-editor-tab-size]").value).toBe("2");
    expect(field("[data-editor-line-endings-on-save]").value).toBe("unchanged");
    expect([...root.querySelectorAll("[data-editor-line-endings-on-save] option")].map((option) => option.textContent)).toEqual(["Not changed", "CRLF", "LF"]);
    expect(field("[data-editor-column-guides-enabled]").checked).toBe(false);
    expect(field("[data-editor-column-guides]").value).toBe("80, 90");
    expect(field("[data-editor-column-guides]").disabled).toBe(true);
    root.querySelector<HTMLButtonElement>("[data-section=theme]")!.click();
    expect(root.querySelector("[data-editor-font-size]")).toBeNull();
  });

  it("persists each line-ending preference across remounts and other editor settings", async () => {
    for (const value of ["crlf", "lf", "unchanged"]) {
      await change("[data-editor-line-endings-on-save]", value);
      expect(fixture.settings.editorLineEndingsOnSave).toBe(value);
      await change("[data-editor-font-size]", "18");
      expect(fixture.settings).toEqual(expect.objectContaining({
        editorLineEndingsOnSave: value, editorTabSize: 2, headers: { retained: "yes" },
      }));
      unmount();
      mount(root);
      await vi.waitFor(() => expect(field("[data-editor-line-endings-on-save]").disabled).toBe(false));
      expect(field("[data-editor-line-endings-on-save]").value).toBe(value);
    }
  });

  it("defaults empty legacy line endings to Not changed", async () => {
    unmount();
    fixture.settings.editorLineEndingsOnSave = "";
    mount(root);
    await vi.waitFor(() => expect(field("[data-editor-line-endings-on-save]").disabled).toBe(false));
    expect(field("[data-editor-line-endings-on-save]").value).toBe("unchanged");
  });

  it("saves sorted guides, retains them while disabled, and restores them after remount", async () => {
    await change("[data-editor-column-guides-enabled]", true);
    expect(field("[data-editor-column-guides]").disabled).toBe(false);
    await change("[data-editor-column-guides]", "120, 90, 80, 90");
    expect(fixture.settings.editorColumnGuides).toEqual([80, 90, 120]);
    expect(field("[data-editor-column-guides]").value).toBe("80, 90, 120");
    await change("[data-editor-column-guides-enabled]", false);
    expect(fixture.settings.editorColumnGuidesEnabled).toBe(false);
    expect(fixture.settings.editorColumnGuides).toEqual([80, 90, 120]);
    unmount();
    mount(root);
    await vi.waitFor(() => expect(field("[data-editor-column-guides-enabled]").disabled).toBe(false));
    expect(field("[data-editor-column-guides]").value).toBe("80, 90, 120");
    await change("[data-editor-column-guides-enabled]", true);
    expect(fixture.settings.editorColumnGuides).toEqual([80, 90, 120]);
  });

  it("shows invalid drafts without persisting them and permits correction", async () => {
    await change("[data-editor-column-guides-enabled]", true);
    api.put.mockClear();
    for (const invalid of ["", "80, 90.5", "0, 90", "80, 10001"]) {
      await change("[data-editor-column-guides]", invalid);
      expect(api.put).not.toHaveBeenCalled();
      expect(field("[data-editor-column-guides]").getAttribute("aria-invalid")).toBe("true");
      expect(root.querySelector<HTMLElement>("#guide-columns-error")!.hidden).toBe(false);
    }
    await change("[data-editor-font-size]", "18");
    expect(fixture.settings.editorColumnGuides).toEqual([80, 90]);
    expect(field("[data-editor-column-guides]").value).toBe("80, 10001");
    await change("[data-editor-column-guides]", "100");
    expect(fixture.settings.editorColumnGuides).toEqual([100]);
    expect(field("[data-editor-column-guides]").getAttribute("aria-invalid")).toBe("false");
  });

  it("retains guides and unrelated settings while changing font and indentation", async () => {
    await change("[data-editor-column-guides-enabled]", true);
    await change("[data-editor-font-size]", "99");
    await change("[data-editor-indent-style]", "tabs");
    await change("[data-editor-tab-size]", "8");
    expect(fixture.settings).toEqual(expect.objectContaining({
      editorFontSize: 30, editorInsertSpaces: false, editorTabSize: 8,
      editorColumnGuidesEnabled: true, editorColumnGuides: [80, 90], headers: { retained: "yes" },
    }));
  });

  it("reports save failures without losing the requested columns", async () => {
    await change("[data-editor-column-guides-enabled]", true);
    api.put.mockRejectedValueOnce(new Error("offline"));
    await change("[data-editor-column-guides]", "100, 120");
    expect(root.textContent).toContain("Save failed: offline");
    expect(field("[data-editor-column-guides]").value).toBe("100, 120");
    expect(fixture.settings.editorColumnGuides).toEqual([80, 90]);
  });
});
