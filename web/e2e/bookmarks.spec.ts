import { expect, test, type Page } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

async function api(page: Page, path: string, body?: unknown, method = body === undefined ? "GET" : "POST") {
  const response = await page.request.fetch(path, { method, data: body });
  const result = await response.json();
  expect(response.ok(), JSON.stringify(result)).toBeTruthy();
  return result.data;
}

test("built-in bookmarks follow code, persist across browsers, and share the Code sidebar", async ({ page, browser }, testInfo) => {
  test.setTimeout(180_000);
  const directory = dirname(fileURLToPath(import.meta.url));
  const runtime = JSON.parse(readFileSync(join(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  const workspace = join(dirname(runtime.workspace), "bookmarks-workspace");
  mkdirSync(join(workspace, "src"), { recursive: true });
  writeFileSync(join(workspace, "main.go"), ["package demo", "", "func Entry() {}", "", "func Second() {}", "", ...Array.from({ length: 35 }, (_, i) => "// code line " + i)].join("\n"));
  writeFileSync(join(workspace, "src", "helper.go"), "package demo\n\nfunc Help() {}\n");
  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const setup = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (setup) {
    await page.getByLabel("Setup code").fill(runtime.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Playwright Bookmarks");
  await page.getByRole("button", { name: setup ? "Finish setup" : "Sign in" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  const { workspace: created } = await api(page, "/api/workspaces", { name: "Bookmarks", mainPath: workspace, folders: [] });
  const workspaceId = created.id as string;
  await api(page, "/api/workspaces/active", { id: workspaceId }, "PUT");
  await page.goto("/#/settings");
  await page.locator('[data-section="plugins"]').click();
  await page.getByRole("button", { name: "Try built-in Bookmarks" }).click();
  await page.locator('[data-plugin-action="approve-stage"][data-scope="global"]').click();
  await expect(page.getByRole("heading", { name: "Bookmarks v1.0.0" })).toBeVisible();
  await page.getByRole("button", { name: "Back to previous view" }).click();
  await expect(page.locator('[data-plugin-nav-section] [data-plugin-id="bookmarks"]')).toBeVisible();
  await page.locator('[data-plugin-nav-section] [data-plugin-id="bookmarks"]').click();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  const panel = page.locator('[data-sidebar-view="bookmarks"]');
  const icon = page.locator('[data-plugin-nav-section] [data-plugin-id="bookmarks"]');
  const rows = panel.locator("[data-bookmark-id]");
  const input = page.locator("[data-monaco-host]").getByRole("textbox", { name: "Editor content" });
  const saved = () => api(page, "/api/plugins/bookmarks/state?workspaceId=" + workspaceId);
  const open = async (name: string) => {
    await page.keyboard.press("Control+p");
    await page.getByLabel("Go to File").fill(name);
    await page.getByRole("option", { name: new RegExp(name.replace(".", "\\.")) }).first().click();
    await expect(page.locator(".code-tab.is-active")).toContainText(name);
  };
  const goLine = async (line: number) => {
    await input.focus();
    await page.keyboard.press("Control+Home");
    for (let i = 1; i < line; i++) await page.keyboard.press("ArrowDown");
    await page.keyboard.press("Home");
  };
  const toggle = async (second = "k") => { await page.keyboard.press("Control+k"); await page.keyboard.press(second); };
  await expect(panel).toContainText("No bookmarks yet.");
  await page.emulateMedia({ colorScheme: "dark" });
  await page.screenshot({ path: testInfo.outputPath("bookmarks-empty-dark.png") });
  await page.emulateMedia({ colorScheme: "light" });
  await page.screenshot({ path: testInfo.outputPath("bookmarks-empty-light.png") });

  await page.keyboard.press("Control+2");
  await open("main.go");
  await goLine(3);
  await toggle();
  await expect.poll(async () => (await saved()).bookmarks.length).toBe(1);
  await expect(panel).toBeHidden();
  await toggle("Control+k");
  await expect.poll(async () => (await saved()).bookmarks.length).toBe(0);
  await toggle("Control+k");
  await expect.poll(async () => (await saved()).bookmarks.length).toBe(1);
  await input.evaluate(node => { node.setAttribute("data-bookmark-session", "preserved"); });
  await icon.click();
  await expect(icon).toHaveClass(/is-active/);
  await expect(input).toHaveAttribute("data-bookmark-session", "preserved");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("func Entry() {}");
  await goLine(5);
  await toggle();
  await expect(rows).toHaveCount(2);
  await rows.first().hover();
  await rows.first().getByRole("button", { name: "Rename bookmark" }).click();
  await panel.getByRole("textbox", { name: "Bookmark name" }).fill("Application entry");
  await panel.getByRole("textbox", { name: "Bookmark name" }).press("Enter");
  await expect(rows.first()).toContainText("Application entry");
  await expect.poll(async () => (await saved()).bookmarks[0].label).toBe("Application entry");
  await rows.first().locator("[data-bookmark-open]").click();
  await expect(page.locator("[data-status=cursor]")).toHaveText("Ln 3, Col 1");
  await expect(input).toBeFocused();
  await expect(page.locator(".echo-bookmark-glyph")).toHaveCount(2);
  await page.keyboard.press("F9");
  const breakpoints = page.locator("[data-monaco-host] .echo-debug-breakpoint");
  await expect(breakpoints).toHaveCount(1);
  const bookmarkBox = await page.locator(".echo-bookmark-glyph").first().boundingBox();
  const breakpointBox = await breakpoints.boundingBox();
  expect(bookmarkBox!.x).toBeLessThan(breakpointBox!.x);
  await page.locator(".echo-bookmark-glyph").first().click();
  await expect(breakpoints).toHaveCount(1);
  await input.focus();
  await page.keyboard.press("F9");
  await expect(breakpoints).toHaveCount(0);

  await goLine(1);
  await page.keyboard.insertText("// inserted\n");
  await expect.poll(async () => (await saved()).bookmarks.map((mark: { line: number }) => mark.line)).toEqual([4, 6]);
  await page.keyboard.press("Control+z");
  await expect.poll(async () => (await saved()).bookmarks.map((mark: { line: number }) => mark.line)).toEqual([3, 5]);
  await page.keyboard.press("Control+Shift+z");
  await expect.poll(async () => (await saved()).bookmarks.map((mark: { line: number }) => mark.line)).toEqual([4, 6]);
  await page.keyboard.press("Control+s");
  await open("helper.go");
  await goLine(3);
  await toggle();
  await expect(panel.locator(".code-bookmark-file")).toHaveCount(2);
  await rows.first().locator("[data-bookmark-open]").click();
  await expect(page.locator("[data-status=cursor]")).toHaveText("Ln 4, Col 1");
  await expect(page.locator(".code-tab.is-active")).toContainText("main.go");
  await page.screenshot({ path: testInfo.outputPath("bookmarks-populated-light.png") });
  await page.emulateMedia({ colorScheme: "dark" });
  await rows.first().hover();
  await page.screenshot({ path: testInfo.outputPath("bookmarks-populated-dark.png") });

  await page.reload();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await expect(rows).toHaveCount(3);
  await expect(rows.first()).toContainText("Application entry");
  await page.keyboard.press("Control+2");
  await page.locator(".code-tree-label", { hasText: /^main.go$/ }).click();
  await page.keyboard.press("F2");
  await page.locator("[data-rename-input]").fill("renamed.go");
  await page.locator("[data-rename-input]").press("Enter");
  await expect.poll(async () => (await saved()).bookmarks[0].ref.path).toBe("renamed.go");
  await icon.click();
  await expect(panel.locator(".code-bookmark-file").first()).toContainText("renamed.go");
  await rows.first().locator("[data-bookmark-open]").click();
  await expect(page.locator("[data-status=cursor]")).toHaveText("Ln 4, Col 1");

  await page.keyboard.press("Control+2");
  const source = page.locator(".code-tree-row").filter({ hasText: "renamed.go" });
  const destination = page.locator('.code-tree-row[data-tree-key$=":src"]');
  await source.dragTo(destination);
  await expect.poll(async () => (await saved()).bookmarks[0].ref.path).toBe("src/renamed.go");
  await icon.click();
  await expect(panel.locator(".code-bookmark-file").filter({ hasText: "renamed.go" })).toContainText("src");

  const secondContext = await browser.newContext({ storageState: await page.context().storageState() });
  const secondPage = await secondContext.newPage();
  await secondPage.goto("/#/code?sidebar=bookmarks");
  await expect(secondPage.locator("[data-bookmark-id]")).toHaveCount(3);
  const firstId = (await saved()).bookmarks[0].id;
  await api(secondPage, "/api/plugins/bookmarks/state?workspaceId=" + workspaceId, { action: "rename", id: firstId, label: "Shared entry" });
  await expect(panel.locator(`[data-bookmark-id="${firstId}"]`)).toContainText("Shared entry");
  await secondContext.close();

  const mobileContext = await browser.newContext({
    storageState: await page.context().storageState(), viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true,
  });
  const mobilePage = await mobileContext.newPage();
  await mobilePage.emulateMedia({ colorScheme: "dark" });
  await mobilePage.goto("/#/code?sidebar=bookmarks");
  await expect(mobilePage.locator("[data-bookmark-id]")).toHaveCount(3);
  const mobilePanel = mobilePage.locator('[data-sidebar-view="bookmarks"]');
  const showMobileBookmarks = async () => {
    await mobilePage.locator("[data-plugin-overflow]").click();
    await mobilePage.locator('[data-plugin-mobile-menu] [data-plugin-id="bookmarks"]').click();
    await expect.poll(async () => Math.round((await mobilePanel.boundingBox())!.x)).toBe(0);
  };
  await showMobileBookmarks();
  await mobilePanel.locator("[data-bookmark-open]").first().click();
  await showMobileBookmarks();
  await expect(mobilePanel.getByRole("button", { name: "Rename bookmark" }).first()).toBeVisible();
  await mobilePage.screenshot({ path: testInfo.outputPath("bookmarks-mobile-dark.png"), animations: "disabled" });
  await mobilePanel.locator("[data-bookmark-open]").first().click();
  await expect(mobilePage.locator(".code-app-shell")).not.toHaveClass(/is-explorer-open/);
  await mobileContext.close();

  const { workspace: secondary } = await api(page, "/api/workspaces", { name: "Bookmark isolation", mainPath: runtime.secondaryWorkspace, folders: [] });
  await api(page, "/api/workspaces/active", { id: secondary.id }, "PUT");
  await page.reload();
  await expect(panel).toContainText("No bookmarks yet.");
  await api(page, "/api/workspaces/active", { id: workspaceId }, "PUT");
  await page.reload();
  await expect(rows).toHaveCount(3);
  await api(page, "/api/plugins/bookmarks/actions", { action: "disable-global" });
  await expect(icon).toHaveCount(0);
  await expect(panel).toBeHidden();
  await expect(page.locator(".echo-bookmark-glyph")).toHaveCount(0);
  await api(page, "/api/plugins/bookmarks/actions", { action: "enable-global" });
  await expect(icon).toBeVisible();
  await icon.click();
  await expect(rows).toHaveCount(3);
  while (await rows.count()) {
    const count = await rows.count();
    await rows.first().hover();
    await rows.first().getByRole("button", { name: "Delete bookmark" }).click();
    await expect(rows).toHaveCount(count - 1);
    await expect.poll(async () => (await saved()).bookmarks.length).toBe(count - 1);
  }
  await expect(panel).toContainText("No bookmarks yet.");
});
