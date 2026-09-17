import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { PersistedTab } from "../src/code/types";

const directory = dirname(fileURLToPath(import.meta.url));
const indentation = "\talpha  beta\tgamma\n  \tmixed\n      remainder\n\t\nend";
const spaces = "    alpha  beta\tgamma\n      mixed\n      remainder\n    \nend";
const tabs = "\talpha  beta\tgamma\n\t  mixed\n\t  remainder\n\t\nend";
let workspace: string;
let workspaceId: string;
let previousLineEndingsOnSave: string | undefined;
let runtime: { setupCode: string; workspace: string; nodePath: string; fakeLSPPath: string };

async function api(page: Page, path: string, method = "GET", data?: unknown) {
  const response = await page.request.fetch(path, { method, data });
  expect(response.ok(), await response.text()).toBe(true);
  return (await response.json()).data;
}

async function command(page: Page, label: string) {
  await page.keyboard.press("Control+Shift+p");
  await page.getByLabel("Command Palette").fill(label);
  await page.getByRole("option").filter({ has: page.getByText(label, { exact: true }) }).click();
}

async function open(page: Page, name: string) {
  await page.keyboard.press("Control+p");
  await page.getByLabel("Go to File").fill(name);
  await page.getByRole("option", { name: new RegExp(name.replace(/\./g, "\\.")) }).first().click();
  await expect(page.locator(".code-tab.is-active")).toContainText(name);
}

async function buffer(page: Page): Promise<PersistedTab | undefined> {
  const activeId = await page.locator(".code-tab.is-active").getAttribute("data-tab-id");
  return page.evaluate(({ id, activeId }) => new Promise((resolve, reject) => {
    const opening = indexedDB.open("echo-code-editor", 2);
    opening.onerror = () => reject(opening.error);
    opening.onsuccess = () => {
      const db = opening.result;
      const request = db.transaction("workspace-sessions").objectStore("workspace-sessions").get(id);
      request.onsuccess = () => {
        db.close();
        resolve(request.result?.activeTabId === activeId
          ? request.result.tabs.find((tab: { id: string }) => tab.id === activeId) : undefined);
      };
      request.onerror = () => { db.close(); reject(request.error); };
    };
  }), { id: workspaceId, activeId });
}

async function expectContent(page: Page, content: string) {
  await expect.poll(async () => (await buffer(page))?.content).toBe(content);
}

function diskContent(name: string): string | null {
  const path = join(workspace, name);
  return existsSync(path) ? readFileSync(path, "utf8") : null;
}

async function save(page: Page, name: string, content: string) {
  const saved = page.waitForResponse((response) => response.url().endsWith("/fs/file") && response.request().method() === "PUT" && response.ok());
  await command(page, "File: Save");
  await saved;
  await expect.poll(() => diskContent(name)).toBe(content);
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).not.toHaveClass(/is-visible/);
}

async function preference(page: Page, value: "unchanged" | "crlf" | "lf") {
  await page.goto("/#/settings?section=code");
  const field = page.getByRole("combobox", { name: "Line endings on save", exact: true });
  await expect(field).toBeEnabled();
  await field.selectOption(value);
  await expect.poll(async () => (await api(page, "/api/settings")).settings.editorLineEndingsOnSave).toBe(value);
  await page.getByRole("button", { name: "Back to previous view" }).click();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
}

async function saveAs(page: Page, name: string, content: string, replace = false) {
  const saved = page.waitForResponse((response) => /\/fs\/(file|entries)$/.test(response.url()) && ["PUT", "POST"].includes(response.request().method()) && response.ok());
  await command(page, "File: Save As…");
  const dialog = page.locator(".code-save-as");
  await dialog.getByLabel("File name").fill(name);
  await dialog.getByRole("button", { name: "Save", exact: true }).click();
  if (replace) await page.getByRole("button", { name: "Replace", exact: true }).click();
  await saved;
  await expect.poll(() => diskContent(name)).toBe(content);
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).not.toHaveClass(/is-visible/);
}

test.beforeEach(async ({ page }, testInfo) => {
  test.setTimeout(120_000);
  previousLineEndingsOnSave = undefined;
  runtime = JSON.parse(readFileSync(join(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  workspace = join(dirname(runtime.workspace), `conversions-${testInfo.testId.slice(-12)}`);
  mkdirSync(workspace, { recursive: true });
  for (const [name, content] of Object.entries({
    "indent.txt": indentation,
    "endings.txt": "\ufefffirst\nsecond\n",
    "plain.txt": "first\nsecond",
    "empty.txt": "",
    "single.txt": "solo",
    "deleted.txt": "delete\nme\n",
    "replace.txt": "old\nfile",
    "diff.txt": "\tbefore\n",
    "main.go": "package main\n\nfunc main() {}\n",
    "picture.svg": '<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20"><rect width="20" height="20"/></svg>',
  })) writeFileSync(join(workspace, name), content);
  const git = (...args: string[]) => execFileSync("git", ["-C", workspace, ...args], { windowsHide: true });
  git("init", "-b", "main");
  git("config", "core.autocrlf", "false");
  git("add", ".");
  git("-c", "user.name=Echo E2E", "-c", "user.email=echo-e2e@example.com", "commit", "-m", "Editor conversions fixture");
  writeFileSync(join(workspace, "diff.txt"), "\tafter\n");

  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const setup = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (setup) {
    await page.getByLabel("Setup code").fill(runtime.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Playwright Editor Conversions");
  await page.getByRole("button", { name: setup ? "Finish setup" : "Sign in" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  const created = await api(page, "/api/workspaces", "POST", { name: `Editor conversions ${testInfo.testId.slice(-12)}`, mainPath: workspace, folders: [] });
  workspaceId = created.workspace.id;
  await api(page, "/api/workspaces/active", "PUT", { id: workspaceId });
  const { settings } = await api(page, "/api/settings");
  previousLineEndingsOnSave = settings.editorLineEndingsOnSave || "unchanged";
  await api(page, "/api/settings", "PUT", { settings: {
    ...settings, editorLineEndingsOnSave: "unchanged", editorInsertSpaces: false, editorTabSize: 4,
  } });
  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
});

test.afterEach(async ({ page }) => {
  if (previousLineEndingsOnSave === undefined) return;
  const { settings } = await api(page, "/api/settings");
  await api(page, "/api/settings", "PUT", { settings: {
    ...settings, editorLineEndingsOnSave: previousLineEndingsOnSave,
  } });
});

test("palette conversions use native indentation, undo, and recovery", async ({ page }) => {
  const status = page.locator('[data-status="eol"]');
  const indentStatus = page.locator('[data-status="indentation"]');
  const input = page.locator("[data-monaco-host]").getByRole("textbox", { name: "Editor content" });
  await open(page, "indent.txt");
  await indentStatus.click();
  await page.getByRole("dialog", { name: "Indentation" }).getByLabel("Indent using").selectOption("tabs");
  await page.getByRole("dialog", { name: "Indentation" }).getByLabel("Width").selectOption("4");
  await page.keyboard.press("Escape");
  await command(page, "Convert tabs to spaces");
  await expectContent(page, spaces);
  await expect(indentStatus).toHaveText("Spaces: 4");
  await expect(input).toBeFocused();
  expect((await api(page, "/api/settings")).settings.editorInsertSpaces).toBe(false);
  await command(page, "Editor: Undo");
  await expectContent(page, indentation);
  await command(page, "Editor: Redo");
  await expectContent(page, spaces);
  await command(page, "Convert spaces to tabs");
  await expectContent(page, tabs);
  await expect(indentStatus).toHaveText("Tabs: 4");
  await command(page, "Editor: Undo");
  await expectContent(page, spaces);
  await indentStatus.click();
  await page.getByRole("dialog", { name: "Indentation" }).getByLabel("Width").selectOption("2");
  await page.keyboard.press("Escape");
  await command(page, "Convert spaces to tabs");
  await save(page, "indent.txt", "\t\talpha  beta\tgamma\n\t\t\tmixed\n\t\t\tremainder\n\t\t\nend");

  await open(page, "endings.txt");
  await command(page, "Convert line endings to CRLF");
  await expect(status).toHaveText("CRLF");
  await expectContent(page, "first\r\nsecond\r\n");
  await command(page, "Editor: Undo");
  await expect(status).toHaveText("LF");
  await command(page, "Editor: Redo");
  await expect(status).toHaveText("CRLF");
  await save(page, "endings.txt", "\ufefffirst\r\nsecond\r\n");
  await command(page, "Convert line endings to CRLF");
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).not.toHaveClass(/is-visible/);
  await command(page, "Editor: Undo");
  await expect(status).toHaveText("LF");
  await command(page, "Convert line endings to CRLF");
  await expectContent(page, "first\r\nsecond\r\n");
  await page.reload();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await expect(status).toHaveText("CRLF");
  await command(page, "Convert line endings to LF");
  await save(page, "endings.txt", "\ufefffirst\nsecond\n");

  // No newline bytes are required to retain an unsaved buffer's selected EOL.
  await command(page, "File: New Untitled File");
  await command(page, "Convert line endings to CRLF");
  await expect.poll(async () => (await buffer(page))?.eol).toBe("crlf");
  await page.reload();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  // Navigation history can reopen the last file-backed location on reload.
  await page.getByRole("tab", { name: /Untitled-1/ }).click();
  await expect(status).toHaveText("CRLF");
  await input.focus();
  await page.keyboard.insertText("one\ntwo");
  await saveAs(page, "untitled.txt", "one\r\ntwo");
  await command(page, "Convert line endings to LF");
  await expectContent(page, "one\ntwo");
  await page.reload();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await page.locator(".code-tab", { hasText: "untitled.txt" }).click();
  await expect(status).toHaveText("LF");
  await save(page, "untitled.txt", "one\ntwo");
});

test("line endings on save cover Save As, retries, recreation, and failed saves", async ({ page }, testInfo) => {
  await open(page, "plain.txt");
  await preference(page, "crlf");
  await save(page, "plain.txt", "first\r\nsecond");
  await command(page, "Convert line endings to LF");
  await saveAs(page, "copy.txt", "first\r\nsecond");
  await command(page, "Convert line endings to LF");
  await saveAs(page, "replace.txt", "first\r\nsecond", true);

  await open(page, "empty.txt");
  await save(page, "empty.txt", "");
  await expect(page.locator('[data-status="eol"]')).toHaveText("CRLF");
  await open(page, "single.txt");
  await save(page, "single.txt", "solo");
  await saveAs(page, "single-copy.txt", "solo");
  await expect(page.locator('[data-status="eol"]')).toHaveText("CRLF");

  await open(page, "deleted.txt");
  await command(page, "Convert line endings to CRLF");
  await expectContent(page, "delete\r\nme\r\n");
  unlinkSync(join(workspace, "deleted.txt"));
  await expect(page.locator('.code-tab.is-active [title="Deleted on disk"]')).toBeVisible();
  await command(page, "File: Save");
  await page.getByRole("button", { name: "Recreate", exact: true }).click();
  await expect.poll(() => diskContent("deleted.txt")).toBe("delete\r\nme\r\n");

  await open(page, "endings.txt");
  await page.route("**/fs/file", (route) => route.request().method() === "PUT"
    ? route.fulfill({ status: 500, json: { ok: false, error: "Simulated save failure" } }) : route.continue());
  await command(page, "File: Save");
  await expect(page.getByText("Simulated save failure", { exact: true })).toBeVisible();
  await expectContent(page, "first\r\nsecond\r\n");
  expect(readFileSync(join(workspace, "endings.txt"), "utf8")).toBe("\ufefffirst\nsecond\n");
  await page.unroute("**/fs/file");
  await page.reload();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await expect(page.locator('[data-status="eol"]')).toHaveText("CRLF");
  writeFileSync(join(workspace, "endings.txt"), "\ufeffexternal\nchange\n");
  await command(page, "File: Save");
  await page.getByRole("button", { name: "Overwrite", exact: true }).click();
  await expect.poll(() => readFileSync(join(workspace, "endings.txt"), "utf8")).toBe("\ufefffirst\r\nsecond\r\n");

  await preference(page, "lf");
  await save(page, "endings.txt", "\ufefffirst\nsecond\n");
  await preference(page, "unchanged");
  await command(page, "Convert line endings to CRLF");
  await save(page, "endings.txt", "\ufefffirst\r\nsecond\r\n");
  await page.goto("/#/settings?section=code");
  await expect(page.getByRole("combobox", { name: "Line endings on save", exact: true })).toHaveValue("unchanged");
  await page.screenshot({ path: testInfo.outputPath("line-endings-settings.png") });
});

test("conversions work in shared editable diffs and ignore read-only and media views", async ({ page }) => {
  await open(page, "diff.txt");
  await save(page, "diff.txt", "\tafter\n");
  const normalId = await page.locator(".code-tab.is-active").getAttribute("data-tab-id");
  await page.getByRole("button", { name: "Source Control", exact: true }).click();
  await page.locator(".git-change-group[data-git-group='unstaged'] .git-change-row", { hasText: "diff.txt" }).dblclick();
  await expect(page.locator("[data-monaco-diff-host]")).toBeVisible();
  const diffId = await page.locator(".code-tab.is-active").getAttribute("data-tab-id");
  await command(page, "Convert tabs to spaces");
  await expectContent(page, "    after\n");
  await command(page, "Editor: Undo");
  await expectContent(page, "\tafter\n");
  await command(page, "Editor: Redo");
  await expectContent(page, "    after\n");
  await command(page, "Convert line endings to CRLF");
  await expectContent(page, "    after\r\n");
  await page.locator(`[data-tab-id="${normalId}"]`).click();
  await expect(page.locator('[data-status="eol"]')).toHaveText("CRLF");
  await expectContent(page, "    after\r\n");
  await page.locator(`[data-tab-id="${diffId}"]`).click();
  await expectContent(page, "    after\r\n");
  await page.reload();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await expect(page.locator("[data-monaco-diff-host]")).toBeVisible();
  await expect(page.locator('[data-status="eol"]')).toHaveText("CRLF");
  await preference(page, "lf");
  await save(page, "diff.txt", "    after\n");

  await command(page, "Convert line endings to CRLF");
  writeFileSync(join(workspace, "diff.txt"), "external\n");
  await command(page, "File: Save");
  await page.getByRole("button", { name: "Overwrite", exact: true }).click();
  await expect.poll(() => readFileSync(join(workspace, "diff.txt"), "utf8")).toBe("    after\n");

  execFileSync("git", ["-C", workspace, "add", "diff.txt"], { windowsHide: true });
  await page.locator(".git-change-group[data-git-group='staged'] .git-change-row", { hasText: "diff.txt" }).dblclick();
  await expect(page.locator("[data-monaco-diff-host]")).toBeVisible();
  await expect(page.locator(".code-tab.is-active")).not.toHaveAttribute("data-tab-id", diffId!);
  for (const label of ["Convert tabs to spaces", "Convert spaces to tabs", "Convert line endings to CRLF", "Convert line endings to LF"]) {
    await command(page, label);
    await expect(page.locator(".code-tab.is-active .code-tab-dirty")).not.toHaveClass(/is-visible/);
  }
  await expect(page.locator('[data-status="eol"]')).toHaveText("LF");
  expect(readFileSync(join(workspace, "diff.txt"), "utf8")).toBe("    after\n");
  await open(page, "picture.svg");
  await expect(page.locator("[data-media-preview-host]")).toBeVisible();
  await command(page, "Convert line endings to CRLF");
  await command(page, "Convert spaces to tabs");
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).not.toHaveClass(/is-visible/);
  await expect(page.locator("[data-media-preview-host]")).toBeVisible();
});

test("save applies line endings after language-server formatting", async ({ page }) => {
  await api(page, "/api/lsp/profiles", "POST", { profile: {
    id: "conversion-lsp", name: "Conversion LSP", command: runtime.nodePath, args: [runtime.fakeLSPPath],
    selectors: [{ languageId: "go", extensions: [".go"] }],
  } });
  await api(page, `/api/workspaces/${workspaceId}/lsp/config`, "PUT", { config: {
    enabledProfileIds: ["conversion-lsp"], formatOnSave: true, formatOnSaveTimeoutMs: 3000,
  } });
  await preference(page, "crlf");
  await open(page, "main.go");
  await expect(page.locator('[data-status="lsp"]')).toContainText("Conversion LSP ✓", { timeout: 20_000 });
  await command(page, "Convert line endings to LF");
  await save(page, "main.go", "// formatted by Echo fake LSP\r\npackage main\r\n\r\nfunc main() {}\r\n");
  await expect(page.locator('[data-status="eol"]')).toHaveText("CRLF");
  // Undo isolates the automatic EOL change from the formatter's text edit.
  await command(page, "Editor: Undo");
  await expectContent(page, "// formatted by Echo fake LSP\npackage main\n\nfunc main() {}\n");
  await expect(page.locator('[data-status="eol"]')).toHaveText("LF");
});
