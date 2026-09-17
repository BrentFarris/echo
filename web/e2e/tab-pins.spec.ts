import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { PersistedWorkspaceSession } from "../src/code/types";

const directory = dirname(fileURLToPath(import.meta.url));
const tab = (page: Page, title: string) => page.getByRole("tab").filter({
  has: page.getByText(title, { exact: true }),
});
const editorInput = (page: Page) => page.locator("[data-monaco-host]").getByRole("textbox", { name: "Editor content" });

async function setup(page: Page, name: string) {
  const state = JSON.parse(readFileSync(join(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  const workspace = join(dirname(state.workspace), `tab-pins-${name}`);
  mkdirSync(workspace, { recursive: true });
  for (const filename of ["a.txt", "b.txt", "c.txt", "change.txt"]) writeFileSync(join(workspace, filename), `${filename}\n`);
  writeFileSync(join(workspace, "image.svg"), '<svg xmlns="http://www.w3.org/2000/svg" width="80" height="80"><rect width="80" height="80" fill="steelblue"/></svg>');
  const git = (...args: string[]) => execFileSync("git", ["-C", workspace, ...args], { windowsHide: true, stdio: "pipe" });
  git("init", "-b", "main");
  git("add", ".");
  git("-c", "user.name=Echo E2E", "-c", "user.email=echo-e2e@example.com", "commit", "-m", "Tab pin fixture");
  writeFileSync(join(workspace, "change.txt"), "changed on disk\n");

  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const firstRun = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (firstRun) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Playwright Tab Pins");
  await page.getByRole("button", { name: firstRun ? "Finish setup" : "Sign in" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  const workspaceId = await page.evaluate(async ({ mainPath, name }) => {
    const request = async (path: string, body: unknown, method = "PUT") => {
      const response = await fetch(path, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      const result = await response.json();
      if (!response.ok || result.ok === false) throw new Error(JSON.stringify(result));
      return result.data;
    };
    const created = await request("/api/workspaces", { name, mainPath, folders: [] }, "POST");
    await request("/api/workspaces/active", { id: created.workspace.id });
    return created.workspace.id as string;
  }, { mainPath: workspace, name: `Tab Pins ${name}` });
  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  return { workspace, workspaceId };
}

async function open(page: Page, name: string) {
  await page.getByRole("treeitem", { name, exact: true }).click();
  await expect(tab(page, name)).toHaveAttribute("aria-selected", "true");
  return tab(page, name);
}

async function pin(page: Page, title: string) {
  await tab(page, title).click({ button: "right" });
  await page.getByRole("menuitem", { name: "Pin", exact: true }).click();
  await expect(tab(page, title)).toHaveClass(/is-pinned/);
}

async function session(page: Page, workspaceId: string): Promise<PersistedWorkspaceSession | undefined> {
  return await page.evaluate((id) => new Promise<PersistedWorkspaceSession | undefined>((resolve, reject) => {
    const request = indexedDB.open("echo-code-editor", 2);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const database = request.result;
      const get = database.transaction("workspace-sessions").objectStore("workspace-sessions").get(id);
      get.onsuccess = () => { database.close(); resolve(get.result); };
      get.onerror = () => { database.close(); reject(get.error); };
    };
  }), workspaceId);
}

test("Keep Open retains previews and Pin protects every tab-close action", async ({ page }) => {
  test.setTimeout(120_000);
  await setup(page, "actions");
  const first = await open(page, "a.txt");
  await expect(first).toHaveClass(/is-preview/);
  for (const checked of [false, true, false]) {
    await first.click({ button: "right" });
    const keepOpen = page.getByRole("menuitemcheckbox", { name: "Keep Open", exact: true });
    await expect(keepOpen).toHaveAttribute("aria-checked", String(checked));
    await keepOpen.click();
    if (checked) await expect(first).toHaveClass(/is-preview/);
    else await expect(first).not.toHaveClass(/is-preview/);
  }
  await open(page, "b.txt");
  const last = await open(page, "c.txt");
  await expect(tab(page, "b.txt")).toHaveCount(0);
  await expect(first).toBeVisible();
  await last.dblclick();
  await expect(last).not.toHaveClass(/is-preview|is-pinned/);

  const titles = page.locator(".code-tab-title");
  const order = await titles.allTextContents();
  await pin(page, "a.txt");
  await expect(titles).toHaveText(order);
  await expect(last).toHaveAttribute("aria-selected", "true");
  const pinButton = first.getByRole("button", { name: "Unpin a.txt", exact: true });
  await expect(pinButton).toBeVisible();
  await expect(pinButton).toHaveCSS("opacity", "1");
  await expect(first.locator("[data-tab-close]")).toHaveCount(0);
  await first.click({ button: "right" });
  await expect(page.getByRole("menuitemcheckbox", { name: "Keep Open" })).toBeChecked();
  await expect(page.getByRole("menuitemcheckbox", { name: "Keep Open" })).toBeDisabled();
  await expect(page.getByRole("menuitem", { name: /^Close\s+Ctrl\+W$/ })).toBeDisabled();
  await page.keyboard.press("Escape");
  await pinButton.click({ button: "middle" });
  await expect(first).toHaveClass(/is-pinned/);
  await expect(last).toHaveAttribute("aria-selected", "true");

  await first.click();
  await editorInput(page).focus();
  await page.keyboard.insertText("unsaved pinned text");
  for (const shortcut of ["Control+w", "Meta+w"]) {
    await page.keyboard.press(shortcut);
    await expect(first).toHaveAttribute("aria-selected", "true");
    await expect(page.getByRole("dialog")).toHaveCount(0);
  }
  await first.click({ button: "middle" });
  await expect(first).toHaveClass(/is-pinned/);
  await page.keyboard.press("Control+Shift+p");
  await page.getByLabel("Command Palette").fill("View: Close Editor");
  await page.getByRole("option", { name: /View: Close Editor/ }).click();
  await expect(first).toHaveAttribute("aria-selected", "true");
  await expect(page.getByRole("dialog")).toHaveCount(0);

  const second = await open(page, "b.txt");
  await editorInput(page).focus();
  await page.keyboard.insertText("unsaved unpinned text");
  await expect(second).not.toHaveClass(/is-preview|is-pinned/);
  await last.click({ button: "right" });
  await page.getByRole("menuitem", { name: "Close Others", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("Close 1 other editor?");
  await page.getByRole("button", { name: "Discard All", exact: true }).click();
  await expect(second).toHaveCount(0);
  await expect(first.locator(".code-tab-dirty")).toHaveClass(/is-visible/);
  await expect(last).toHaveAttribute("aria-selected", "true");
  await last.click({ button: "right" });
  await page.getByRole("menuitem", { name: "Close Others", exact: true }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(first).toHaveClass(/is-pinned/);
  await last.hover();
  await expect(pinButton).toBeVisible();
  await expect(pinButton).toHaveCSS("opacity", "1");

  // The action button must not initiate the parent's tab drag.
  const start = (await pinButton.boundingBox())!;
  const end = (await last.boundingBox())!;
  await page.mouse.move(start.x + start.width / 2, start.y + start.height / 2);
  await page.mouse.down();
  try {
    await page.mouse.move(end.x + end.width / 2, end.y + end.height / 2, { steps: 8 });
    await expect(page.locator(".code-tab.is-dragging")).toHaveCount(0);
  } finally {
    await page.mouse.up();
  }
  await expect(titles).toHaveText(order);
  await expect(first).toHaveClass(/is-pinned/);
  await pinButton.focus();
  await page.keyboard.press("Space");
  const closeButton = first.getByRole("button", { name: "Close a.txt", exact: true });
  await expect(closeButton).toBeFocused();
  await expect(first).not.toHaveClass(/is-preview|is-pinned/);
  await expect(last).toHaveAttribute("aria-selected", "true");
  await pin(page, "a.txt");
  await pinButton.click();
  await expect(first).not.toHaveClass(/is-preview|is-pinned/);
  await expect(last).toHaveAttribute("aria-selected", "true");
  await pin(page, "a.txt");
  await first.click({ button: "right" });
  await page.getByRole("menuitem", { name: "Unpin", exact: true }).click();
  await expect(first).not.toHaveClass(/is-preview|is-pinned/);
  await first.hover();
  await closeButton.click();
  await page.getByRole("button", { name: "Discard", exact: true }).click();
  await expect(first).toHaveCount(0);
  await last.click({ button: "middle" });
  await expect(last).toHaveCount(0);
});

test("restores pins on files, untitled buffers, media and diffs, and reads legacy sessions", async ({ page }, testInfo) => {
  test.setTimeout(120_000);
  const { workspaceId } = await setup(page, "recovery");
  await open(page, "a.txt");
  await pin(page, "a.txt");
  await editorInput(page).focus();
  await page.keyboard.insertText("recover this unsaved text");
  await open(page, "image.svg");
  await pin(page, "image.svg");
  await page.keyboard.press("Control+n");
  await editorInput(page).focus();
  await page.keyboard.insertText("recover this untitled buffer");
  await pin(page, "Untitled-1");
  await page.getByRole("button", { name: "Source Control", exact: true }).click();
  await page.locator(".git-change-group[data-git-group='unstaged'] .git-change-row", { hasText: "change.txt" }).click();
  await expect(page.locator(".code-tab.is-active .code-tab-title")).toContainText("change.txt");
  const diffTitle = (await page.locator(".code-tab.is-active .code-tab-title").textContent())!;
  await expect(page.locator("[data-monaco-diff-host]")).toBeVisible();
  await pin(page, diffTitle);
  await page.getByRole("button", { name: "Explorer", exact: true }).click();
  const kept = await open(page, "b.txt");
  await kept.dblclick();
  await open(page, "c.txt");
  const order = await page.locator(".code-tab-title").allTextContents();
  const pinnedTitles = ["a.txt", "image.svg", "Untitled-1", diffTitle].sort();
  await expect.poll(async () => (await session(page, workspaceId))?.tabs.filter((item) => item.closeProtected).map((item) => item.title).sort()).toEqual(pinnedTitles);
  await page.reload();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await expect(page.locator(".code-tab-title")).toHaveText(order);
  for (const title of pinnedTitles) {
    await expect(tab(page, title)).toHaveClass(/is-pinned/);
    await expect(tab(page, title).getByRole("button", { name: `Unpin ${title}`, exact: true })).toBeVisible();
  }
  await expect(kept).not.toHaveClass(/is-pinned|is-preview/);
  await expect(tab(page, "c.txt")).toHaveClass(/is-preview/);
  await tab(page, "a.txt").click();
  await expect(page.locator("[data-monaco-host] .view-lines")).toContainText("recover this unsaved text");
  await tab(page, "Untitled-1").click();
  await expect(page.locator("[data-monaco-host] .view-lines")).toContainText("recover this untitled buffer");
  await page.screenshot({ path: testInfo.outputPath("pinned-tabs.png") });

  // Leave Code before replacing the stored payload so its final save cannot
  // race the legacy fixture. Old `pinned` fields must only mean Keep Open.
  await page.goto("/#/settings");
  await expect(page.locator(".settings-view")).toBeVisible();
  await page.evaluate((id) => new Promise<void>((resolve, reject) => {
    const request = indexedDB.open("echo-code-editor", 2);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const database = request.result;
      const transaction = database.transaction("workspace-sessions", "readwrite");
      const store = transaction.objectStore("workspace-sessions");
      const get = store.get(id);
      get.onsuccess = () => {
        const saved = get.result as PersistedWorkspaceSession;
        for (const item of saved.tabs) delete item.closeProtected;
        store.put(saved, id);
      };
      transaction.oncomplete = () => { database.close(); resolve(); };
      transaction.onerror = () => { database.close(); reject(transaction.error); };
    };
  }), workspaceId);
  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await expect(page.locator(".code-tab-title")).toHaveText(order);
  await expect(page.locator(".code-tab.is-pinned")).toHaveCount(0);
  for (const title of pinnedTitles) await expect(tab(page, title)).not.toHaveClass(/is-preview/);
  await expect(tab(page, "c.txt")).toHaveClass(/is-preview/);
  await kept.click();
  await page.keyboard.press("Control+w");
  await expect(kept).toHaveCount(0);
});

test("Save As preserves either tab's pin and confirmed deletion removes pinned tabs", async ({ page }) => {
  test.setTimeout(120_000);
  const { workspace } = await setup(page, "cleanup");
  for (const [filename, pinDestination] of [["a.txt", true], ["b.txt", false]] as const) {
    const destination = await open(page, filename);
    await destination.dblclick();
    if (pinDestination) await pin(page, filename);
    await page.keyboard.press("Control+n");
    await editorInput(page).focus();
    await page.keyboard.insertText(`replacement for ${filename}`);
    if (!pinDestination) {
      const title = (await page.locator(".code-tab.is-active .code-tab-title").textContent())!;
      await pin(page, title);
    }
    await page.keyboard.press("Control+s");
    await page.getByRole("dialog").getByLabel("File name").fill(filename);
    await page.getByRole("button", { name: "Save", exact: true }).click();
    await page.getByRole("button", { name: "Replace", exact: true }).click();
    await expect(destination).toHaveCount(1);
    await expect(destination).toHaveAttribute("aria-selected", "true");
    await expect(destination).toHaveClass(/is-pinned/);
    await expect(destination.locator(".code-tab-dirty")).not.toHaveClass(/is-visible/);
    expect(readFileSync(join(workspace, filename), "utf8")).toBe(`replacement for ${filename}`);
    await page.keyboard.press("Control+w");
    await expect(destination).toHaveCount(1);
  }
  await page.getByRole("treeitem", { name: "a.txt", exact: true }).click({ button: "right" });
  await page.getByRole("menuitem", { name: /^Delete/ }).click();
  await page.getByRole("button", { name: "Move to Trash", exact: true }).click();
  await expect(tab(page, "a.txt")).toHaveCount(0);
  expect(existsSync(join(workspace, "a.txt"))).toBe(false);
  await expect(tab(page, "b.txt")).toHaveClass(/is-pinned/);
});
