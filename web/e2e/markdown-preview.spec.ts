import { expect, test, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import type { PersistedWorkspaceSession } from "../src/code/types";

const directory = dirname(fileURLToPath(import.meta.url));
const source = (page: Page) => page.locator("[data-monaco-host]");
const preview = (page: Page) => page.getByRole("region", { name: "Markdown preview", exact: true });
const modes = (page: Page) => page.getByRole("group", { name: "Markdown view" });
const mode = (page: Page, name: string) => modes(page).getByRole("button", { name, exact: true });
const input = (page: Page) => source(page).getByRole("textbox", { name: "Editor content" });
const active = (page: Page) => page.locator(".code-tab.is-active");

async function setup(page: Page, name: string) {
  const state = JSON.parse(readFileSync(join(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  const workspace = join(dirname(state.workspace), `markdown-${name}`);
  mkdirSync(join(workspace, "assets"), { recursive: true });
  const markdown = [
    "# Guide", "", "[Deep section](#deep-section) · [Other document](other.MARKDOWN) · [Text file](notes.txt)", "",
    '<p align="center"><img src="assets/pixel.png" alt="Local badge" width="24"></p>', "",
    "![Local image](assets/pixel.png)", "", "| Feature | Status |", "| --- | --- |", "| Preview | Ready |", "",
    "- [x] Live preview", "- [ ] Publish", "", "```ts", "const answer = 42;", "```", "",
    "<details><summary>More information</summary><p>Safe embedded HTML.</p></details>", "",
    ...Array.from({ length: 80 }, (_, index) => `Paragraph ${index + 1}. A document with enough content to scroll independently.\n`),
    "## Deep section", "", "End of the guide.", "",
  ].join("\n");
  writeFileSync(join(workspace, "guide.md"), markdown);
  writeFileSync(join(workspace, "other.MARKDOWN"), "# Other document\n\nA second file.\n");
  writeFileSync(join(workspace, "notes.txt"), "Plain text\n");
  writeFileSync(join(workspace, "assets", "pixel.png"), Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=", "base64"));
  const git = (...args: string[]) => execFileSync("git", ["-C", workspace, ...args], { windowsHide: true, stdio: "pipe" });
  git("init", "-b", "main");
  git("add", ".");
  git("-c", "user.name=Echo E2E", "-c", "user.email=echo-e2e@example.com", "commit", "-m", "Markdown fixture");
  writeFileSync(join(workspace, "notes.txt"), "Changed plain text\n");

  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const firstRun = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (firstRun) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Playwright Markdown");
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
  }, { mainPath: workspace, name: `Markdown ${name}` });
  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  return { workspace, workspaceId };
}

async function open(page: Page, name: string) {
  await page.keyboard.press("Control+p");
  await page.getByLabel("Go to File").fill(name);
  await page.getByRole("option", { name: new RegExp(name.replaceAll(".", "\\.")) }).first().click();
  await expect(active(page)).toContainText(name);
}

async function session(page: Page, workspaceId: string): Promise<PersistedWorkspaceSession | undefined> {
  return page.evaluate(id => new Promise<PersistedWorkspaceSession | undefined>((resolve, reject) => {
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

test("source, live split and full preview preserve unsaved edits, cursor and undo", async ({ page }, testInfo) => {
  test.setTimeout(120_000);
  const { workspace } = await setup(page, "editing");
  await open(page, "guide.md");
  await expect(mode(page, "Source")).toHaveAttribute("aria-pressed", "true");
  await expect(preview(page)).toBeHidden();
  await mode(page, "Split").click();
  await expect(source(page)).toBeVisible();
  await expect(preview(page).getByRole("heading", { name: "Guide", exact: true })).toBeVisible();
  expect((await source(page).boundingBox())!.x).toBeLessThan((await preview(page).boundingBox())!.x);
  await expect.poll(() => preview(page).getByAltText("Local image").evaluate((image: HTMLImageElement) => image.naturalWidth)).toBe(1);
  await preview(page).getByText("More information", { exact: true }).click();
  await expect(preview(page).getByText("Safe embedded HTML.")).toBeVisible();
  await input(page).focus();
  await page.keyboard.press("Control+Home");
  await page.keyboard.press("End");
  await page.keyboard.insertText(" unsaved");
  await expect(preview(page).getByRole("heading", { name: "Guide unsaved", exact: true })).toBeVisible();
  expect(readFileSync(join(workspace, "guide.md"), "utf8")).not.toContain("unsaved");
  const cursor = await page.locator('[data-status="cursor"]').textContent();
  await mode(page, "Preview").click();
  await expect(source(page)).toBeHidden();
  await expect(preview(page)).toBeFocused();
  await mode(page, "Source").click();
  await expect(page.locator('[data-status="cursor"]')).toHaveText(cursor!);
  await page.keyboard.press("Control+z");
  await mode(page, "Split").click();
  await expect(preview(page).getByRole("heading", { name: "Guide", exact: true })).toBeVisible();
  await expect(preview(page).getByRole("heading", { name: "Guide unsaved", exact: true })).toHaveCount(0);
  await input(page).focus();
  await page.keyboard.press("Control+Home");
  await page.keyboard.insertText("Saved from preview.\n\n");
  await mode(page, "Preview").click();
  await page.keyboard.press("Control+s");
  await expect.poll(() => readFileSync(join(workspace, "guide.md"), "utf8")).toContain("Saved from preview.");
  await mode(page, "Split").click();
  await page.screenshot({ path: testInfo.outputPath("markdown-split-light.png"), animations: "disabled" });
  await page.emulateMedia({ colorScheme: "dark" });
  await page.screenshot({ path: testInfo.outputPath("markdown-split-dark.png"), animations: "disabled" });
  await page.setViewportSize({ width: 390, height: 844 });
  await mode(page, "Preview").click();
  await expect(preview(page)).toBeVisible();
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await expect.poll(() => page.locator(".code-sidebar").evaluate(element => element.getBoundingClientRect().right)).toBeLessThanOrEqual(0);
  await page.screenshot({ path: testInfo.outputPath("markdown-preview-mobile.png"), animations: "disabled" });
});

test("independent scrolling, resizing and per-tab modes survive reload", async ({ page }) => {
  test.setTimeout(120_000);
  const { workspaceId } = await setup(page, "state");
  await open(page, "guide.md");
  await mode(page, "Split").click();
  await input(page).focus();
  await page.keyboard.press("Control+Home");
  await page.keyboard.press("Shift+End");
  await page.keyboard.insertText("# Restored buffer");
  await expect(preview(page).getByRole("heading", { name: "Restored buffer" })).toBeVisible();
  await preview(page).evaluate(element => { element.scrollTop = 450; });
  await input(page).focus();
  await page.keyboard.press("Control+End");
  await expect.poll(() => preview(page).evaluate(element => element.scrollTop)).toBe(450);
  const divider = page.getByRole("separator", { name: "Resize Markdown source" });
  await divider.focus();
  await page.keyboard.press("ArrowRight");
  await page.keyboard.press("ArrowRight");
  await expect(divider).toHaveAttribute("aria-valuenow", "60");
  const bounds = (await divider.boundingBox())!;
  await page.mouse.move(bounds.x + bounds.width / 2, bounds.y + 40);
  await page.mouse.down();
  await page.mouse.move(bounds.x - 60, bounds.y + 40);
  await page.mouse.up();
  await expect(divider).not.toHaveAttribute("aria-valuenow", "60");
  const ratio = await divider.getAttribute("aria-valuenow");
  await mode(page, "Preview").click();
  await preview(page).evaluate(element => { element.scrollTop = 600; });
  await expect.poll(async () => (await session(page, workspaceId))?.tabs.find(tab => tab.title === "guide.md")?.markdownPreviewScrollTop).toBe(600);
  await open(page, "other.MARKDOWN");
  await expect(mode(page, "Source")).toHaveAttribute("aria-pressed", "true");
  await open(page, "guide.md");
  await expect(mode(page, "Preview")).toHaveAttribute("aria-pressed", "true");
  await expect.poll(() => preview(page).evaluate(element => element.scrollTop)).toBe(600);
  await expect(preview(page).getByRole("heading", { name: "Restored buffer" })).toHaveCount(1);
  await expect.poll(async () => (await session(page, workspaceId))?.activeTabId).toBe(await active(page).getAttribute("data-tab-id"));
  await page.reload();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await expect(mode(page, "Preview")).toHaveAttribute("aria-pressed", "true");
  await expect.poll(() => preview(page).evaluate(element => element.scrollTop)).toBe(600);
  await mode(page, "Split").click();
  await expect(divider).toHaveAttribute("aria-valuenow", ratio!);
  await expect(page.locator('[data-status="cursor"]')).not.toHaveText("Ln 1, Col 1");
});

test("links, file changes, source navigation, media and diffs use the appropriate surface", async ({ page }) => {
  test.setTimeout(120_000);
  const { workspace } = await setup(page, "navigation");
  await open(page, "guide.md");
  await mode(page, "Preview").click();
  const route = page.url();
  await preview(page).getByRole("link", { name: "Deep section", exact: true }).click();
  expect(page.url()).toBe(route);
  await expect.poll(() => preview(page).evaluate(element => element.scrollTop)).toBeGreaterThan(1000);
  await preview(page).evaluate(element => { element.scrollTop = 0; });
  await preview(page).getByRole("link", { name: "Other document", exact: true }).click();
  await expect(active(page)).toContainText("other.MARKDOWN");
  await expect(mode(page, "Source")).toHaveAttribute("aria-pressed", "true");
  await mode(page, "Preview").click();
  writeFileSync(join(workspace, "other.MARKDOWN"), "# Changed externally\n");
  await expect(preview(page).getByRole("heading", { name: "Changed externally" })).toBeVisible({ timeout: 20_000 });
  await page.keyboard.press("Control+f");
  await expect(mode(page, "Split")).toHaveAttribute("aria-pressed", "true");
  await expect(source(page).getByRole("textbox", { name: "Find", exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await mode(page, "Preview").click();
  await page.keyboard.press("Control+g");
  await expect(mode(page, "Split")).toHaveAttribute("aria-pressed", "true");
  await page.keyboard.press("Escape");
  await mode(page, "Preview").click();
  await page.keyboard.press("Control+Shift+f");
  await page.getByLabel("Search workspace", { exact: true }).fill("Changed externally");
  await page.locator("[data-search-result]").filter({ hasText: "Changed externally" }).click();
  await expect(mode(page, "Split")).toHaveAttribute("aria-pressed", "true");
  await expect(input(page)).toBeFocused();
  await open(page, "notes.txt");
  await expect(modes(page)).toBeHidden();
  await expect(source(page)).toBeVisible();
  await open(page, "pixel.png");
  await expect(page.locator("[data-media-preview-host]")).toBeVisible();
  await expect(modes(page)).toBeHidden();
  await expect(source(page)).toBeHidden();
  await page.locator('[data-code-sidebar="git"]').first().click();
  await page.locator(".git-change-group[data-git-group='unstaged'] .git-change-row", { hasText: "notes.txt" }).click();
  await expect(page.locator("[data-monaco-diff-host]")).toBeVisible();
  await expect(modes(page)).toBeHidden();
});

test("Save As and rename reevaluate Markdown availability without losing the buffer", async ({ page }) => {
  test.setTimeout(120_000);
  const { workspace } = await setup(page, "rename");
  await open(page, "notes.txt");
  await page.keyboard.press("Control+Shift+s");
  await page.getByRole("dialog").getByLabel("File name").fill("saved.md");
  await page.getByRole("dialog").getByRole("button", { name: "Save", exact: true }).click();
  await expect(active(page)).toContainText("saved.md");
  await mode(page, "Split").click();
  await expect(preview(page)).toContainText("Changed plain text");
  await input(page).focus();
  await page.keyboard.press("Control+Home");
  await page.keyboard.insertText("# Renamed\n\n");
  await mode(page, "Preview").click();
  const row = page.getByRole("treeitem", { name: "saved.md", exact: true });
  await row.click();
  await page.keyboard.press("F2");
  await page.locator("[data-rename-input]").fill("saved.txt");
  await page.locator("[data-rename-input]").press("Enter");
  await expect(active(page)).toContainText("saved.txt");
  await expect(modes(page)).toBeHidden();
  await expect(source(page)).toBeVisible();
  await page.keyboard.press("Control+s");
  await expect.poll(() => readFileSync(join(workspace, "saved.txt"), "utf8")).toContain("# Renamed");
});
