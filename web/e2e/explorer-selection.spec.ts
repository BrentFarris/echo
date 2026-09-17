import { expect, test, type Locator, type Page } from "@playwright/test";
import { existsSync, mkdirSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));
const row = (page: Page, name: string) => page.getByRole("treeitem", { name, exact: true });
const selected = (page: Page) => page.locator('[data-code-tree] [aria-selected="true"]');

async function setup(page: Page, name: string) {
  const state = JSON.parse(readFileSync(join(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  const workspace = join(dirname(state.workspace), `explorer-selection-${name}`);
  for (const folder of ["destination", "folder", "many"]) mkdirSync(join(workspace, folder), { recursive: true });
  for (const filename of ["a.txt", "b.txt", "c.txt", "d.txt", "folder/child.txt"]) writeFileSync(join(workspace, filename), `${filename}\n`);
  for (let index = 0; index < 160; index++) writeFileSync(join(workspace, "many", `file-${String(index).padStart(3, "0")}.txt`), `${index}\n`);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const firstRun = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (firstRun) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Playwright Explorer Selection");
  await page.getByRole("button", { name: firstRun ? "Finish setup" : "Sign in" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  await page.evaluate(async ({ mainPath, name }) => {
    const created = await (await fetch("/api/workspaces", { method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, mainPath, folders: [] }) })).json();
    await fetch("/api/workspaces/active", { method: "PUT", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: created.data.workspace.id }) });
  }, { mainPath: workspace, name: `Explorer Selection ${name}` });
  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  return workspace;
}

async function drag(page: Page, source: Locator, target: Locator, count: number) {
  const from = await source.boundingBox();
  const to = await target.boundingBox();
  expect(from).not.toBeNull();
  expect(to).not.toBeNull();
  await page.mouse.move(from!.x + from!.width / 2, from!.y + from!.height / 2);
  await page.mouse.down();
  try {
    await page.mouse.move(from!.x + from!.width / 2 + 8, from!.y + from!.height / 2, { steps: 2 });
    await page.mouse.move(to!.x + to!.width / 2, to!.y + to!.height / 2, { steps: 8 });
    await expect(page.locator(".code-tree-row.is-dragging")).toHaveCount(count);
  } finally { await page.mouse.up(); }
}

async function edit(page: Page, name: string, content: string) {
  await row(page, name).dblclick();
  await expect(page.locator(".code-tab.is-active")).toContainText(name);
  const input = page.locator("[data-monaco-host]").getByRole("textbox", { name: "Editor content" });
  await input.focus();
  await page.keyboard.press("Control+End");
  await page.keyboard.insertText(content);
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).toHaveClass(/is-visible/);
}

test("modifier gestures, context menus, folders and keyboard keep selection distinct from the editor", async ({ page }) => {
  const workspace = await setup(page, "gestures");
  await expect(page.getByRole("tree", { name: "Workspace files" })).toHaveAttribute("aria-multiselectable", "true");
  await row(page, "b.txt").click();
  await expect(page.locator(".code-tab.is-active")).toContainText("b.txt");
  await row(page, "d.txt").click({ modifiers: ["Shift"] });
  await expect(selected(page)).toHaveText(["b.txt", "c.txt", "d.txt"]);
  await row(page, "a.txt").click({ modifiers: ["Shift"] });
  await expect(selected(page)).toHaveText(["a.txt", "b.txt"]);
  await row(page, "d.txt").click({ modifiers: ["Control"] });
  await row(page, "c.txt").click({ modifiers: ["Control", "Shift"] });
  await expect(selected(page)).toHaveText(["a.txt", "b.txt", "c.txt", "d.txt"]);
  await row(page, "b.txt").click({ modifiers: ["Meta"] });
  await expect(selected(page)).toHaveText(["a.txt", "c.txt", "d.txt"]);
  await row(page, "c.txt").dblclick({ modifiers: ["Control"] });
  await expect(selected(page)).toHaveText(["a.txt", "c.txt", "d.txt"]);
  await expect(page.locator(".code-tab.is-active")).toContainText("b.txt");
  await expect(page.locator(".code-tab")).toHaveCount(1);
  await expect(page.locator(".code-tab.is-preview")).toHaveCount(1);
  await row(page, "c.txt").click({ button: "right" });
  await expect(selected(page)).toHaveCount(3);
  await expect(page.getByRole("menuitem", { name: "Rename F2" })).toBeDisabled();
  await page.keyboard.press("Escape");
  await row(page, "b.txt").click({ button: "right" });
  await expect(selected(page)).toHaveText(["b.txt"]);
  await page.keyboard.press("Escape");
  await row(page, "b.txt").click({ modifiers: ["Control"] });
  await expect(selected(page)).toHaveCount(0);
  await page.keyboard.press("Delete");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await page.keyboard.press("ArrowDown");
  await expect(selected(page)).toHaveText(["c.txt"]);
  await row(page, "folder").click({ modifiers: ["Control"] });
  await expect(row(page, "folder")).toHaveAttribute("aria-expanded", "false");
  await page.getByRole("button", { name: "Refresh Explorer", exact: true }).click();
  await expect(selected(page)).toHaveText(["folder", "c.txt"]);
  unlinkSync(join(workspace, "c.txt"));
  await page.getByRole("button", { name: "Refresh Explorer", exact: true }).click();
  await expect(row(page, "c.txt")).toHaveCount(0);
  await expect(selected(page)).toHaveText(["folder"]);
  await row(page, "folder").click();
  await row(page, "child.txt").click({ modifiers: ["Control"] });
  await expect(selected(page)).toHaveText(["folder", "child.txt"]);
  await page.getByRole("button", { name: "Collapse All", exact: true }).click();
  await expect(selected(page)).toHaveCount(0);
});

test("ranges include virtualized rows and late file reads do not replace a newer selection", async ({ page }) => {
  await setup(page, "virtualized");
  let release!: () => void;
  let requested = false;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/fs/file?**", async (route) => {
    if (new URL(route.request().url()).searchParams.get("path") !== "a.txt") return route.continue();
    const response = await route.fetch();
    requested = true;
    await gate;
    await route.fulfill({ response });
  });
  await row(page, "a.txt").click();
  await expect.poll(() => requested).toBe(true);
  await row(page, "b.txt").click({ modifiers: ["Control"] });
  release();
  await expect(page.locator(".code-tab.is-active")).toContainText("a.txt");
  await expect(selected(page)).toHaveText(["a.txt", "b.txt"]);
  await page.unroute("**/fs/file?**");
  await row(page, "many").click();
  await row(page, "file-000.txt").click();
  await expect(page.locator(".code-tab.is-active")).toContainText("file-000.txt");
  const tree = page.locator("[data-code-tree]");
  await tree.evaluate((element) => { element.scrollTop = 22 * 99; });
  await row(page, "file-100.txt").click({ modifiers: ["Shift"] });
  await expect(row(page, "file-100.txt")).toHaveAttribute("aria-selected", "true");
  await expect(row(page, "file-101.txt")).toHaveAttribute("aria-selected", "false");
  await page.keyboard.press("Delete");
  await expect(page.getByRole("dialog", { name: "Delete 101 items?" })).toBeVisible();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await tree.evaluate((element) => { element.scrollTop = 22 * 49; });
  await expect(row(page, "file-050.txt")).toHaveAttribute("aria-selected", "true");
  await page.getByRole("button", { name: "Refresh Explorer", exact: true }).click();
  await expect(row(page, "file-050.txt")).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".code-tab.is-active")).toContainText("file-000.txt");
});

test("group delete saves dirty files once, deduplicates folders and restores the group with Undo", async ({ page }) => {
  const workspace = await setup(page, "delete");
  const deletes: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "DELETE" && request.url().endsWith("/fs/entry")) deletes.push(request.postDataJSON().ref.path);
  });
  await edit(page, "a.txt", "unsaved a\n");
  await row(page, "folder").click();
  await edit(page, "child.txt", "unsaved child\n");
  await row(page, "folder").click({ modifiers: ["Control"] });
  await row(page, "a.txt").click({ modifiers: ["Control"] });
  await page.keyboard.press("Delete");
  await expect(page.getByRole("dialog", { name: "Delete 2 items?" })).toBeVisible();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  expect(deletes).toEqual([]);
  await page.locator("[data-code-tree]").focus();
  await page.keyboard.press("Delete");
  await page.getByRole("button", { name: "Save All, Then Delete", exact: true }).click();
  await expect.poll(() => existsSync(join(workspace, "a.txt"))).toBe(false);
  await expect.poll(() => existsSync(join(workspace, "folder"))).toBe(false);
  expect(deletes.sort()).toEqual(["a.txt", "folder"]);
  await expect(page.locator(".code-tab")).toHaveCount(0);
  await page.getByRole("button", { name: "Undo", exact: true }).click();
  await expect.poll(() => existsSync(join(workspace, "folder", "child.txt"))).toBe(true);
  expect(readFileSync(join(workspace, "a.txt"), "utf8")).toContain("unsaved a");
  expect(readFileSync(join(workspace, "folder", "child.txt"), "utf8")).toContain("unsaved child");
});

test("partial delete keeps failed tabs and Undo reports collisions without overwriting", async ({ page }) => {
  const workspace = await setup(page, "delete-failures");
  await edit(page, "a.txt", "keep failed edit\n");
  await row(page, "b.txt").click({ modifiers: ["Control"] });
  await row(page, "c.txt").click({ modifiers: ["Control"] });
  await page.route("**/fs/entry", (route) => route.request().method() === "DELETE" && route.request().postDataJSON().ref.path === "a.txt"
    ? route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ ok: false, error: "delete test failure" }) }) : route.continue());
  await page.keyboard.press("Delete");
  await page.getByRole("button", { name: "Discard changes and delete", exact: true }).click();
  await expect.poll(() => existsSync(join(workspace, "b.txt"))).toBe(false);
  await expect.poll(() => existsSync(join(workspace, "c.txt"))).toBe(false);
  expect(existsSync(join(workspace, "a.txt"))).toBe(true);
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).toHaveClass(/is-visible/);
  await expect(page.locator("[data-monaco-host] .view-lines")).toContainText("keep failed edit");
  await expect(page.locator(".code-toast").filter({ hasText: "delete test failure" })).toBeVisible();
  writeFileSync(join(workspace, "b.txt"), "replacement must survive\n");
  await page.getByRole("button", { name: "Undo", exact: true }).click();
  await expect.poll(() => existsSync(join(workspace, "c.txt"))).toBe(true);
  expect(readFileSync(join(workspace, "b.txt"), "utf8")).toBe("replacement must survive\n");
  await expect(page.locator(".code-toast").filter({ hasText: /Restored 1 item.*Failed: b.txt/ })).toBeVisible();
});

test("group drag moves folders once, retains dirty editors and leaves collisions in place", async ({ page }) => {
  const workspace = await setup(page, "move");
  writeFileSync(join(workspace, "destination", "b.txt"), "existing destination\n");
  const moves: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "PATCH" && request.url().endsWith("/fs/entry") && request.postDataJSON().destinationParent) moves.push(request.postDataJSON().ref.path);
  });
  await row(page, "folder").click();
  await edit(page, "child.txt", "dirty moved child\n");
  await row(page, "folder").click({ modifiers: ["Control"] });
  await row(page, "a.txt").click({ modifiers: ["Control"] });
  await row(page, "b.txt").click({ modifiers: ["Control"] });
  await row(page, "c.txt").click({ modifiers: ["Control"] });
  await drag(page, row(page, "a.txt"), row(page, "destination"), 5);
  await expect.poll(() => existsSync(join(workspace, "destination", "folder", "child.txt"))).toBe(true);
  await expect.poll(() => existsSync(join(workspace, "destination", "a.txt"))).toBe(true);
  await expect.poll(() => existsSync(join(workspace, "destination", "c.txt"))).toBe(true);
  expect(moves.sort()).toEqual(["a.txt", "b.txt", "c.txt", "folder"]);
  expect(existsSync(join(workspace, "b.txt"))).toBe(true);
  expect(readFileSync(join(workspace, "destination", "b.txt"), "utf8")).toBe("existing destination\n");
  await expect(page.locator(".code-toast").filter({ hasText: /Moved 3 items.*Failed: b.txt/ })).toBeVisible();
  await expect(page.locator("[data-monaco-host] .view-lines")).toContainText("dirty moved child");
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).toHaveClass(/is-visible/);
  await expect(selected(page)).toHaveCount(5);
  await page.keyboard.press("Control+s");
  await expect.poll(() => readFileSync(join(workspace, "destination", "folder", "child.txt"), "utf8")).toContain("dirty moved child");
  expect(existsSync(join(workspace, "folder"))).toBe(false);
});

test("protected members disable group operations and single-row actions remain scoped", async ({ page }) => {
  const workspace = await setup(page, "protected");
  await row(page, "a.txt").click({ modifiers: ["Control"] });
  const root = page.locator('[data-tree-root="true"]').first();
  await root.click({ modifiers: ["Control"] });
  await row(page, "a.txt").click({ button: "right" });
  await expect(page.getByRole("menuitem", { name: /Delete/ })).toBeDisabled();
  await expect(page.getByRole("menuitem", { name: /Rename/ })).toBeDisabled();
  await page.keyboard.press("Escape");
  await page.locator("[data-code-tree]").focus();
  await page.keyboard.press("Delete");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  expect(existsSync(join(workspace, "a.txt"))).toBe(true);
  await drag(page, row(page, "a.txt"), row(page, "destination"), 0);
  expect(existsSync(join(workspace, "destination", "a.txt"))).toBe(false);
  await row(page, "folder").click({ button: "right" });
  await expect(selected(page)).toHaveText(["folder"]);
  await page.getByRole("menuitem", { name: "New File", exact: true }).click();
  await page.getByRole("dialog").getByLabel("Name", { exact: true }).fill("created.txt");
  await page.getByRole("button", { name: "Create", exact: true }).click();
  await expect.poll(() => existsSync(join(workspace, "folder", "created.txt"))).toBe(true);
  await expect(row(page, "created.txt")).toHaveAttribute("aria-selected", "true");
});

test("a delayed tab reveal cannot clear a newer selection, and Rename uses the sole selected item", async ({ page }) => {
  await setup(page, "reveal");
  await row(page, "a.txt").dblclick();
  await row(page, "b.txt").dblclick();
  await expect(page.locator(".code-tab.is-active")).toContainText("b.txt");
  let release!: () => void;
  let requested = false;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/fs/entries?**", async (route) => {
    if (new URL(route.request().url()).searchParams.get("path") !== "") return route.continue();
    const response = await route.fetch();
    requested = true;
    await gate;
    await route.fulfill({ response });
  });
  await page.getByRole("button", { name: "Refresh Explorer", exact: true }).click();
  await expect.poll(() => requested).toBe(true);
  await page.locator(".code-tab").filter({ hasText: "a.txt" }).click();
  await expect(page.locator(".code-tab.is-active")).toContainText("a.txt");
  await row(page, "c.txt").click({ modifiers: ["Control"] });
  const listing = page.waitForResponse((response) => response.url().includes("/fs/entries?")
    && new URL(response.url()).searchParams.get("path") === "");
  release();
  await (await listing).finished();
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  await page.unroute("**/fs/entries?**");
  await expect(selected(page)).toHaveText(["b.txt", "c.txt"]);
  await row(page, "c.txt").click({ modifiers: ["Control"] });
  await page.keyboard.press("F2");
  await expect(page.getByRole("textbox", { name: "Rename b.txt" })).toBeVisible();
  await page.getByRole("textbox", { name: "Rename b.txt" }).press("Escape");
  await expect(selected(page)).toHaveText(["b.txt"]);
});

test("a failed save cancels the whole delete, and in-flight group operations cannot overlap", async ({ page }) => {
  const workspace = await setup(page, "preflight");
  await edit(page, "a.txt", "saved before delete\n");
  await edit(page, "b.txt", "must stay dirty\n");
  await row(page, "a.txt").click({ modifiers: ["Control"] });
  const deletes: string[] = [];
  page.on("request", (request) => {
    if (request.method() === "DELETE" && request.url().endsWith("/fs/entry")) deletes.push(request.postDataJSON().ref.path);
  });
  await page.route("**/fs/file", (route) => route.request().method() === "PUT" && route.request().postDataJSON().ref.path === "b.txt"
    ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ ok: false, error: "save test failure" }) }) : route.continue());
  await page.keyboard.press("Delete");
  await page.getByRole("button", { name: "Save All, Then Delete", exact: true }).click();
  await expect(page.locator(".code-toast").filter({ hasText: "save test failure" })).toBeVisible();
  expect(deletes).toEqual([]);
  expect(readFileSync(join(workspace, "a.txt"), "utf8")).toContain("saved before delete");
  expect(readFileSync(join(workspace, "b.txt"), "utf8")).not.toContain("must stay dirty");
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).toHaveClass(/is-visible/);
  await page.unroute("**/fs/file");
  let release!: () => void;
  const gate = new Promise<void>((resolve) => { release = resolve; });
  await page.route("**/fs/entry", async (route) => {
    if (route.request().method() === "DELETE") await gate;
    await route.continue();
  });
  await page.locator("[data-code-tree]").focus();
  await page.keyboard.press("Delete");
  await page.getByRole("button", { name: "Discard changes and delete", exact: true }).click();
  await expect.poll(() => deletes.length).toBe(1);
  await row(page, "c.txt").click({ button: "right" });
  await expect(page.getByRole("menuitem", { name: /Delete/ })).toBeDisabled();
  await expect(page.getByRole("menuitem", { name: /Rename/ })).toBeDisabled();
  await page.keyboard.press("Escape");
  await page.locator("[data-code-tree]").focus();
  await page.keyboard.press("Delete");
  await expect(page.getByRole("dialog")).toHaveCount(0);
  release();
  await expect.poll(() => existsSync(join(workspace, "a.txt"))).toBe(false);
  await expect.poll(() => existsSync(join(workspace, "b.txt"))).toBe(false);
  expect(deletes.sort()).toEqual(["a.txt", "b.txt"]);
  expect(existsSync(join(workspace, "c.txt"))).toBe(true);
  await expect(row(page, "c.txt")).toHaveAttribute("aria-selected", "true");
});
