import { expect, test } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { basename, dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

test("Ctrl+P opens files at a line and preserves navigation history", async ({ page }) => {
  const directory = dirname(fileURLToPath(import.meta.url));
  const state = JSON.parse(readFileSync(join(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  const workspace = join(dirname(state.workspace), "quick-open-workspace");
  mkdirSync(join(workspace, "nested"), { recursive: true });
  const content = ["package main", "", ...Array.from({ length: 100 }, (_, index) => `// navigation line ${index + 3}`)].join("\n");
  writeFileSync(join(workspace, "main.go"), content);
  writeFileSync(join(workspace, "nested", "other.go"), content);
  writeFileSync(join(workspace, "guide.md"), content);

  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const firstRun = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (firstRun) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Playwright Quick Open");
  await page.getByRole("button", { name: firstRun ? "Finish setup" : "Sign in" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  const previousWorkspaceId = await page.evaluate(async mainPath => {
    const current = await (await fetch("/api/workspaces")).json();
    const response = await fetch("/api/workspaces", {
      method: "POST", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "Quick Open", mainPath, folders: [] }),
    });
    const created = await response.json();
    if (!response.ok || !created.data?.workspace) throw new Error("Could not create quick-open workspace");
    const activated = await fetch("/api/workspaces/active", {
      method: "PUT", headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ id: created.data.workspace.id }),
    });
    if (!activated.ok) throw new Error("Could not activate quick-open workspace");
    return current.data?.activeId as string | undefined;
  }, workspace);

  try {
    await page.goto("/#/code");
    await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
    const cursorStatus = page.locator('[data-status="cursor"]');
    const editorHost = page.locator("[data-monaco-host]");
    const editorInput = editorHost.getByRole("textbox", { name: "Editor content" });
    const quickOpenInput = page.getByLabel("Go to File");
    const quickOpen = async (value: string, query = "main.go", click = false) => {
      await page.keyboard.press("Control+p");
      const searched = page.waitForResponse(response => {
        const url = new URL(response.url());
        return url.pathname.endsWith("/fs/search") && url.searchParams.get("q") === query;
      });
      await quickOpenInput.fill(value);
      await searched;
      const result = page.getByRole("option").filter({ hasText: basename(query) });
      await expect(result).toBeVisible();
      if (click) await result.click();
      else await quickOpenInput.press("Enter");
      await expect(quickOpenInput).toHaveCount(0);
      await expect(page.locator(".code-tab.is-active")).toContainText(basename(query));
      await expect(editorInput).toBeFocused();
    };

    // Open a new file at a line, then jump and scroll within the existing tab.
    await quickOpen("main.go:18");
    await expect(cursorStatus).toHaveText("Ln 18, Col 1");
    await quickOpen("main.go:90", "main.go", true);
    await expect(cursorStatus).toHaveText("Ln 90, Col 1");
    await expect(editorHost.locator(".line-numbers").filter({ hasText: /^90$/ })).toBeInViewport();
    await expect(editorHost.locator(".view-lines")).not.toContainText("package main");
    await expect(page.getByRole("tab", { name: /main\.go/ })).toHaveCount(1);

    // Each jump records its final caret position and viewport for Back/Forward.
    await page.goBack();
    await expect(cursorStatus).toHaveText("Ln 18, Col 1");
    await page.goForward();
    await expect(cursorStatus).toHaveText("Ln 90, Col 1");
    await expect(editorHost.locator(".line-numbers").filter({ hasText: /^90$/ })).toBeInViewport();

    for (const [value, line] of [
      ["main.go:", 90], ["main.go", 90], ["main.go:0", 1], ["main.go:999", 102],
    ] as const) {
      await quickOpen(value);
      await expect(cursorStatus).toHaveText(`Ln ${line}, Col 1`);
    }
    await quickOpen("nested/other.go:18", "nested/other.go");
    await expect(cursorStatus).toHaveText("Ln 18, Col 1");

    // A line target reveals source when an existing Markdown tab shows preview.
    await quickOpen("guide.md", "guide.md");
    await page.getByRole("group", { name: "Markdown view" }).getByRole("button", { name: "Preview", exact: true }).click();
    await expect(editorHost).toBeHidden();
    await quickOpen("guide.md:90", "guide.md");
    await expect(editorHost).toBeVisible();
    await expect(cursorStatus).toHaveText("Ln 90, Col 1");
    await expect(editorHost.locator(".line-numbers").filter({ hasText: /^90$/ })).toBeInViewport();
    expect(readFileSync(join(workspace, "main.go"), "utf8")).toBe(content);
  } finally {
    if (previousWorkspaceId) {
      await page.evaluate(async id => {
        const response = await fetch("/api/workspaces/active", {
          method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id }),
        });
        if (!response.ok) throw new Error("Could not restore active workspace");
      }, previousWorkspaceId);
    }
  }
});
