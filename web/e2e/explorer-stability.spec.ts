import { expect, test, type Page, type WebSocketRoute } from "@playwright/test";
import { mkdirSync, readFileSync, renameSync, unlinkSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

async function settleTree(page: Page): Promise<void> {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}

async function startProbe(page: Page): Promise<void> {
  await settleTree(page);
  await page.evaluate(() => {
    const tree = document.querySelector<HTMLElement>("[data-code-tree]")!;
    const canvas = tree.querySelector<HTMLElement>("[data-tree-canvas]")!;
    const rows = [...canvas.children];
    const selected = rows.map((row) => row.getAttribute("aria-selected"));
    const scrollTop = tree.scrollTop;
    const height = canvas.style.height;
    const failures = new Set<string>();
    let frame = 0;
    const check = () => {
      if (Math.abs(tree.scrollTop - scrollTop) > 0.5) failures.add(`scroll moved from ${scrollTop} to ${tree.scrollTop}`);
      if (canvas.style.height !== height) failures.add(`height changed from ${height} to ${canvas.style.height}`);
      if (canvas.children.length !== rows.length || rows.some((row, index) => row !== canvas.children[index])) failures.add("visible rows were replaced");
      if (rows.some((row, index) => row.getAttribute("aria-selected") !== selected[index])) failures.add("selection changed");
      if (canvas.querySelector(".codicon-loading")) failures.add("loaded folder showed a spinner");
    };
    const observer = new MutationObserver((records) => {
      if (records.some((record) => [...record.removedNodes].some((node) => rows.includes(node as Element)))) failures.add("existing rows were detached");
      check();
    });
    observer.observe(canvas, { childList: true, subtree: true, attributes: true });
    const tick = () => { check(); frame = requestAnimationFrame(tick); };
    tick();
    (window as any).__explorerProbe = () => {
      check();
      cancelAnimationFrame(frame);
      observer.disconnect();
      return [...failures];
    };
  });
}

async function stopProbe(page: Page): Promise<void> {
  await settleTree(page);
  expect(await page.evaluate(() => (window as any).__explorerProbe())).toEqual([]);
}

test("saves and background refreshes keep the explorer viewport and rows stable", async ({ page }, testInfo) => {
  test.setTimeout(120_000);
  const directory = dirname(fileURLToPath(import.meta.url));
  const state = JSON.parse(readFileSync(join(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  const workspace = join(dirname(state.workspace), "explorer-stability");
  const nested = join(workspace, "group", "nested");
  mkdirSync(nested, { recursive: true });
  for (let i = 0; i < 140; i++) writeFileSync(join(nested, `file-${String(i).padStart(3, "0")}.ts`), `export const value = ${i};\n`);
  writeFileSync(join(workspace, "main.ts"), "export const main = true;\n");

  let socket: WebSocketRoute | undefined;
  let sequence = 0;
  const changes: Array<{ ref: { path: string } }> = [];
  await page.routeWebSocket((url) => url.pathname === "/ws", (client) => {
    socket = client;
    const server = client.connectToServer();
    server.onMessage((message) => {
      const event = JSON.parse(String(message));
      if (event.type === "workspace_fs_changed") {
        sequence = event.sequence;
        changes.push(...(event.changes || []));
      }
      client.send(message);
    });
  });
  const pending = new Set<string>();
  let listings = 0;
  page.on("request", (request) => { if (request.url().includes("/fs/entries?")) { pending.add(request.url()); listings++; } });
  page.on("requestfinished", (request) => pending.delete(request.url()));
  page.on("requestfailed", (request) => pending.delete(request.url()));
  const settleRefresh = async () => {
    // Watcher batches arrive every 100ms; include subsequent atomic-save notifications.
    await page.waitForTimeout(350);
    await expect.poll(() => pending.size).toBe(0);
    await settleTree(page);
  };
  const waitForChange = async (path: string, action: () => Promise<unknown> | void) => {
    const start = changes.length;
    await action();
    await expect.poll(() => changes.slice(start).some((change) => change.ref.path === path)).toBe(true);
    await settleRefresh();
  };

  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const setup = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (setup) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Playwright Explorer");
  await page.getByRole("button", { name: setup ? "Finish setup" : "Sign in" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  const workspaceId = await page.evaluate(async (mainPath) => {
    const created = await (await fetch("/api/workspaces", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: "Explorer stability", mainPath, folders: [] }),
    })).json();
    const id = created.data.workspace.id;
    await fetch("/api/workspaces/active", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id }) });
    return id as string;
  }, workspace);
  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await page.getByRole("treeitem", { name: "group", exact: true }).click();
  await page.getByRole("treeitem", { name: "nested", exact: true }).click();
  const tree = page.locator("[data-code-tree]");
  const canvas = page.locator("[data-tree-canvas]");
  await tree.evaluate((element) => { element.scrollTop = 1001; });
  await page.getByRole("treeitem", { name: "file-050.ts", exact: true }).dblclick();
  await expect(page.locator(".code-tab.is-active")).toContainText("file-050.ts");
  const input = page.locator("[data-monaco-host]").getByRole("textbox", { name: "Editor content" });
  await input.focus();
  await settleRefresh();

  await startProbe(page);
  for (let i = 0; i < 3; i++) {
    await page.keyboard.press("Control+End");
    await page.keyboard.insertText(`// saved ${i}\n`);
    await waitForChange("group/nested/file-050.ts", () => page.keyboard.press("Control+s"));
    await expect.poll(() => readFileSync(join(nested, "file-050.ts"), "utf8")).toContain(`// saved ${i}`);
    await expect(page.locator(".code-tab.is-active .code-tab-dirty")).not.toHaveClass(/is-visible/);
  }
  await stopProbe(page);

  // Writes still reload clean editors without refreshing directory listings.
  await startProbe(page);
  const beforeWrite = listings;
  await waitForChange("group/nested/file-050.ts", () => writeFileSync(join(nested, "file-050.ts"), "export const external = true;\n"));
  await expect(page.locator("[data-monaco-host] .view-lines")).toContainText("external");
  expect(listings).toBe(beforeWrite);
  await stopProbe(page);

  await startProbe(page);
  await waitForChange("main.ts", () => {
    const temporary = join(workspace, ".replacement.tmp");
    writeFileSync(temporary, "export const replaced = true;\n");
    renameSync(temporary, join(workspace, "main.ts"));
  });
  await page.getByRole("button", { name: "Refresh Explorer", exact: true }).click();
  await settleRefresh();
  await stopProbe(page);

  // A slow refresh must preserve scrolling performed after the request started.
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
  await tree.evaluate((element) => { element.scrollTop += 132; });
  await startProbe(page);
  release();
  await settleRefresh();
  await stopProbe(page);
  await page.unroute("**/fs/entries?**");

  // Failed requests leave the last successful listing visible.
  await page.route("**/fs/entries?**", (route) => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ ok: false, error: "Explorer test unavailable" }) }));
  await startProbe(page);
  await page.getByRole("button", { name: "Refresh Explorer", exact: true }).click();
  await settleRefresh();
  await stopProbe(page);
  await page.unroute("**/fs/entries?**");

  // A real insertion above the viewport anchors the visible file, including its partial-row offset.
  const beforeInsert = await tree.evaluate((element) => element.scrollTop);
  const heightBefore = await canvas.evaluate((element) => element.style.height);
  await waitForChange("group/nested/a-before.ts", () => writeFileSync(join(nested, "a-before.ts"), "// added\n"));
  await expect.poll(() => tree.evaluate((element) => element.scrollTop)).toBe(beforeInsert + 22);
  await waitForChange("group/nested/a-before.ts", () => unlinkSync(join(nested, "a-before.ts")));
  await expect.poll(() => tree.evaluate((element) => element.scrollTop)).toBe(beforeInsert);
  await expect(canvas).toHaveCSS("height", heightBefore);

  // Exercise the actual resync handler and its periodic refresh path.
  await startProbe(page);
  const beforeResync = listings;
  socket!.send(JSON.stringify({ type: "fs_resync_required", workspaceId, sequence }));
  await expect.poll(() => listings).toBeGreaterThan(beforeResync);
  await settleRefresh();
  const afterResync = listings;
  await expect.poll(() => listings, { timeout: 5_000 }).toBeGreaterThan(afterResync);
  await settleRefresh();
  await stopProbe(page);
  await page.screenshot({ path: testInfo.outputPath("explorer-after-saves.png") });

  // Explicit navigation still reveals files; refresh preserves collapsed roots.
  await page.keyboard.press("Control+p");
  await page.getByLabel("Go to File").fill("main.ts");
  await page.getByRole("option", { name: /main.ts/ }).first().click();
  await expect(page.getByRole("treeitem", { name: "main.ts", exact: true })).toHaveAttribute("aria-selected", "true");
  await tree.evaluate((element) => { element.scrollTop = 0; });
  await expect(page.getByRole("treeitem", { name: "group", exact: true })).toHaveAttribute("aria-expanded", "true");
  await expect(page.getByRole("treeitem", { name: "nested", exact: true })).toHaveAttribute("aria-expanded", "true");
  const root = page.locator('[data-tree-root="true"]').first();
  await root.click();
  await expect(root).toHaveAttribute("aria-expanded", "false");
  await page.getByRole("button", { name: "Refresh Explorer", exact: true }).click();
  await settleRefresh();
  await expect(root).toHaveAttribute("aria-expanded", "false");
  await expect(canvas).toHaveCSS("height", "22px");
});
