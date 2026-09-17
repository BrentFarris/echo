import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));
const explorer = (page: Page) => page.getByRole("tree", { name: "Workspace files", exact: true });
const row = (page: Page, name: string) => explorer(page).getByRole("treeitem", {
  name: new RegExp(`^${name.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}(?:, has (?:errors|warnings))?$`),
});
const outline = (page: Page) => page.getByRole("region", { name: "Outline", exact: true });
const toggle = (page: Page) => outline(page).getByRole("button", { name: "OUTLINE", exact: true });
const symbol = (page: Page, name: string) => outline(page).locator(".code-outline-name").filter({ hasText: new RegExp(`^${name}$`) });
const ready = (page: Page) => expect(outline(page).getByRole("tree")).toHaveAttribute("aria-busy", "false", { timeout: 25000 });

async function api(page: Page, path: string, method = "GET", data?: unknown) {
  const response = await page.request.fetch(path, { method, data });
  expect(response.ok(), await response.text()).toBe(true);
  return (await response.json()).data;
}

async function setup(page: Page, name: string) {
  const runtime = JSON.parse(readFileSync(join(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  const workspace = join(dirname(runtime.workspace), `outline-${name}`);
  mkdirSync(join(workspace, "folder", "nested"), { recursive: true });
  mkdirSync(join(workspace, "go"), { recursive: true });
  for (const [path, content] of Object.entries({
    "alpha.ts": "export function Alpha() { return 1; }\n",
    "beta.ts": "export class Beta {\n  method() { return 2; }\n}\n",
    "folder/one.ts": "export function FolderOne() {}\n",
    "folder/nested/two.ts": "export function FolderTwo() {}\n",
    "unsupported.txt": "Just some prose.\n",
    "go.mod": "module outline-fixture\n\ngo 1.24\n",
    "go/first.go": "package main\n\nfunc FirstGo() {}\n",
    "go/flat.go": "package main\n\nfunc FlatGo() {}\n",
    "go/slow.go": "package main\n\nfunc SlowGo() {}\n",
    "large.ts": Array.from({ length: 1200 }, (_, index) => `export function Symbol${index}() {}`).join("\n"),
  })) writeFileSync(join(workspace, path), content);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const first = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (first) {
    await page.getByLabel("Setup code").fill(runtime.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Playwright Outline");
  await page.getByRole("button", { name: first ? "Finish setup" : "Sign in" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  const created = await api(page, "/api/workspaces", "POST", { name: `Outline ${name}`, mainPath: workspace, folders: [] });
  const id = created.workspace.id as string;
  await api(page, "/api/workspaces/active", "PUT", { id });
  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  return { runtime, workspace, id };
}

test("built-in symbols load lazily, navigate, virtualize, and preserve only the panel size", async ({ page }, testInfo) => {
  test.setTimeout(90000);
  const { id } = await setup(page, "built-in");
  const reads: string[] = [];
  page.on("request", (request) => {
    if (request.url().includes("/fs/file?")) reads.push(new URL(request.url()).searchParams.get("path")!);
  });
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
  await row(page, "beta.ts").click({ modifiers: ["Control"] });
  expect(reads).toEqual([]);
  await expect(page.locator(".code-tab")).toHaveCount(0);
  await toggle(page).click(); await ready(page);
  await expect(symbol(page, "Beta")).toBeVisible();
  await expect(page.locator(".code-tab")).toHaveCount(0);
  await outline(page).getByRole("tree").focus();
  await page.keyboard.press("ArrowRight");
  await expect(symbol(page, "method")).toBeVisible();
  await page.keyboard.press("ArrowDown"); await page.keyboard.press("Enter");
  await expect(page.locator(".code-tab.is-active")).toContainText("beta.ts");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 2, Col 3");

  await toggle(page).click();
  // A modifier selection can target a closed file without creating another tab.
  await row(page, "beta.ts").click({ modifiers: ["Control"] });
  await row(page, "large.ts").click({ modifiers: ["Control"] });
  await toggle(page).click(); await ready(page);
  await expect(symbol(page, "Symbol0")).toBeVisible();
  expect(await outline(page).getByRole("treeitem").count()).toBeLessThan(40);
  await outline(page).getByRole("tree").focus(); await page.keyboard.press("End");
  await expect(symbol(page, "Symbol1199")).toBeVisible();
  expect(await outline(page).getByRole("treeitem").count()).toBeLessThan(40);

  const separator = page.getByRole("separator", { name: "Resize Outline" });
  const before = (await outline(page).boundingBox())!.height;
  const box = (await separator.boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.down(); await page.mouse.move(box.x + box.width / 2, box.y - 70, { steps: 6 }); await page.mouse.up();
  expect((await outline(page).boundingBox())!.height).toBeGreaterThan(before + 40);
  await separator.focus(); await page.keyboard.press("ArrowUp");
  const ratio = Number(await separator.getAttribute("aria-valuenow"));
  await expect.poll(() => page.evaluate((workspaceId) => new Promise<number | undefined>((resolve) => {
    const request = indexedDB.open("echo-code-editor", 2);
    request.onsuccess = () => {
      const db = request.result;
      const get = db.transaction("workspace-sessions").objectStore("workspace-sessions").get(workspaceId);
      get.onsuccess = () => { db.close(); resolve(Math.round(get.result?.outlineSizeRatio * 100)); };
    };
  }), id)).toBe(ratio);
  await page.screenshot({ path: testInfo.outputPath("outline-expanded.png") });
  await page.reload();
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
  await toggle(page).click();
  await expect(separator).toHaveAttribute("aria-valuenow", String(ratio));
});

test("multi-file and recursive outlines use unsaved buffers and update only on reopening", async ({ page }) => {
  test.setTimeout(90000);
  await setup(page, "snapshot");
  await row(page, "alpha.ts").dblclick();
  const editor = page.locator("[data-monaco-host]").getByRole("textbox", { name: "Editor content" });
  await editor.focus(); await page.keyboard.press("Control+End");
  await page.keyboard.insertText("\nexport function Unsaved() {}\n");
  await row(page, "beta.ts").click({ modifiers: ["Control"] });
  await toggle(page).click(); await ready(page);
  await expect(symbol(page, "Unsaved")).toBeVisible();
  await expect(symbol(page, "Beta")).toBeVisible();
  await expect(page.locator(".code-tab")).toHaveCount(1);
  await row(page, "folder").click({ modifiers: ["Control"] });
  await editor.focus(); await page.keyboard.press("Control+End");
  await page.keyboard.insertText("\nexport function Later() {}\n");
  await expect(symbol(page, "Later")).toHaveCount(0);
  await expect(symbol(page, "FolderOne")).toHaveCount(0);
  await toggle(page).click(); await toggle(page).click(); await ready(page);
  await expect(symbol(page, "Later")).toBeVisible();
  const tree = outline(page).getByRole("tree");
  await tree.focus(); await page.keyboard.press("End");
  await expect(symbol(page, "FolderOne")).toBeVisible();
  await expect(symbol(page, "FolderTwo")).toBeVisible();
  await expect(row(page, "folder")).toHaveAttribute("aria-expanded", "false");
  await expect(row(page, "nested")).toHaveCount(0);
  await expect(page.locator(".code-tab.is-active")).toContainText("alpha.ts");
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).toHaveClass(/is-visible/);
});

test("closing Outline aborts reads and a later selection cannot receive stale results", async ({ page }) => {
  await setup(page, "cancel");
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  let started!: () => void;
  const reading = new Promise<void>((resolve) => { started = resolve; });
  await page.route("**/fs/file?**", async (route) => {
    if (new URL(route.request().url()).searchParams.get("path") !== "beta.ts") return route.continue();
    started(); await held; await route.continue().catch(() => {});
  });
  await row(page, "beta.ts").click({ modifiers: ["Control"] });
  await toggle(page).click(); await reading;
  await expect(outline(page)).toContainText("Loading");
  await toggle(page).click();
  await row(page, "beta.ts").click({ modifiers: ["Control"] });
  await row(page, "alpha.ts").click({ modifiers: ["Control"] });
  await toggle(page).click(); await ready(page);
  await expect(symbol(page, "Alpha")).toBeVisible();
  release();
  await page.unrouteAll({ behavior: "wait" });
  await expect(symbol(page, "Beta")).toHaveCount(0);
  await expect(page.locator(".code-tab")).toHaveCount(0);
});

test("LSP outlines prepare unopened files, preserve a concurrently opened model, and cancel requests", async ({ page }) => {
  test.setTimeout(90000);
  const traffic: Array<Record<string, any>> = [];
  page.on("websocket", (socket) => socket.on("framesent", ({ payload }) => {
    try { traffic.push(JSON.parse(String(payload))); } catch { /* unrelated traffic */ }
  }));
  const { runtime, id } = await setup(page, "lsp");
  const profileId = "outline-e2e-lsp";
  await api(page, "/api/lsp/profiles", "POST", { profile: {
    id: profileId, name: "Outline Fake LSP", command: runtime.nodePath, args: [runtime.fakeLSPPath],
    selectors: [{ languageId: "go", extensions: [".go"] }], environment: { ECHO_FAKE_LSP_SYMBOL_DELAY_MS: "2000" },
  } });
  await api(page, `/api/workspaces/${id}/lsp/config`, "PUT", { config: { enabledProfileIds: [profileId] } });
  await expect.poll(async () => (await api(page, `/api/workspaces/${id}/lsp/config`)).statuses.find((status: { profileId: string }) => status.profileId === profileId)?.state).toBe("running");
  await row(page, "go").click({ modifiers: ["Control"] });
  expect(traffic.filter((message) => message.method === "textDocument/documentSymbol")).toHaveLength(0);
  await toggle(page).click();
  await expect.poll(() => traffic.filter((message) => message.method === "textDocument/documentSymbol").length).toBe(3);
  await expect(page.locator(".code-tab")).toHaveCount(0);
  await row(page, "go").click();
  await row(page, "slow.go").dblclick();
  await ready(page);
  await expect(symbol(page, "SlowGo")).toBeVisible();
  await expect(symbol(page, "FirstGo")).toBeVisible();
  await expect(symbol(page, "FlatGo")).toBeVisible();
  await expect(page.locator(".code-tab.is-active")).toContainText("slow.go");
  const editor = page.locator("[data-monaco-host]").getByRole("textbox", { name: "Editor content" });
  await editor.focus(); await page.keyboard.press("Control+End");
  await page.keyboard.insertText("\nfunc StillEditable() {}\n");
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).toHaveClass(/is-visible/);
  expect(traffic.some((message) => message.type === "lsp_claim" && message.takeOver)).toBe(false);
  expect(traffic.filter((message) => message.type === "lsp_close" && /(?:first|flat)\.go$/.test(message.uri)).length).toBe(2);
  expect(traffic.filter((message) => message.type === "lsp_close" && /slow\.go$/.test(message.uri))).toHaveLength(0);
  await toggle(page).click();
  const prior = traffic.filter((message) => message.method === "textDocument/documentSymbol").length;
  await toggle(page).click();
  await expect.poll(() => traffic.filter((message) => message.method === "textDocument/documentSymbol").length).toBeGreaterThan(prior);
  await toggle(page).click();
  await expect.poll(() => traffic.filter((message) => message.type === "lsp_cancel").length).toBeGreaterThan(0);
  await toggle(page).click(); await ready(page);
  await expect(symbol(page, "StillEditable")).toBeVisible();
});
