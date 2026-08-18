import { expect, test } from "@playwright/test";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));
const password = "Echo-E2E-Password!";

test("first-run auth and the real Monaco filesystem workflow", async ({ page }) => {
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8")) as {
    setupCode: string;
    workspace: string;
  };
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Secure this Echo server" })).toBeVisible();
  await page.getByLabel("Setup code").fill(state.setupCode);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Confirm password").fill(password);
  await page.getByLabel("Device name").fill("Playwright Chromium");
  await page.getByRole("button", { name: "Finish setup" }).click();
  await expect(page.getByRole("button", { name: "Code view" })).toBeVisible();

  const workspaceID = await page.evaluate(async (workspacePath) => {
    const request = async (path: string, init: RequestInit) => {
      const response = await fetch(path, { ...init, headers: { "Content-Type": "application/json" } });
      const payload = await response.json();
      if (!response.ok || payload.ok === false) throw new Error(payload.error || `HTTP ${response.status}`);
      return payload.data;
    };
    const created = await request("/api/workspaces", {
      method: "POST",
      body: JSON.stringify({ name: "E2E Workspace", mainPath: workspacePath, folders: [] }),
    });
    await request("/api/workspaces/active", {
      method: "PUT",
      body: JSON.stringify({ id: created.workspace.id }),
    });
    return created.workspace.id as string;
  }, state.workspace);
  expect(workspaceID).toBeTruthy();

  await page.getByRole("button", { name: "Code view" }).click();
  await expect(page.locator(".code-app-shell")).toBeVisible();
  await expect(page.locator(".code-tree-label", { hasText: "main.go" })).toBeVisible();
  await page.locator(".code-tree-label", { hasText: "main.go" }).click();
  await expect(page.locator(".code-tab.is-preview", { hasText: "main.go" })).toBeVisible();
  await page.locator(".code-tree-label", { hasText: "nested" }).click();
  await page.locator(".code-tree-label", { hasText: "demo.py" }).click();
  await expect(page.getByRole("tab", { name: /main\.go/ })).toHaveCount(0);
  await page.keyboard.press("F2");
  await page.locator("[data-rename-input]").fill("renamed.py");
  await page.locator("[data-rename-input]").press("Enter");
  await expect.poll(() => existsSync(join(state.workspace, "nested", "renamed.py"))).toBe(true);
  await expect.poll(() => existsSync(join(state.workspace, "nested", "demo.py"))).toBe(false);
  await page.locator(".code-tree-label", { hasText: "renamed.py" }).dblclick();
  await expect(page.locator(".code-tab.is-preview", { hasText: "renamed.py" })).toHaveCount(0);

  await page.keyboard.press("Control+p");
  await page.getByLabel("Go to File").fill("main.go");
  await expect(page.getByRole("option", { name: /main\.go/ })).toBeVisible();
  await page.getByLabel("Go to File").press("Enter");
  await expect(page.getByRole("tab", { name: /main\.go/ })).toBeVisible();

  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+End");
  await page.keyboard.press("Enter");
  await page.keyboard.type("// saved by Playwright");
  await page.keyboard.press("Control+s");
  const mainPath = join(state.workspace, "main.go");
  await expect.poll(() => readFileSync(mainPath, "utf8")).toContain("saved by Playwright");

  writeFileSync(mainPath, "package main\n\n// external reload\nfunc main() {}\n", "utf8");
  await expect(page.locator(".view-lines")).toContainText("external reload");

  await page.locator(".code-tree-label", { hasText: "main.go" }).click({ button: "right" });
  await page.getByRole("menuitem", { name: /Delete/ }).click();
  await page.getByRole("button", { name: "Move to Trash" }).click();
  await expect.poll(() => existsSync(mainPath)).toBe(false);
  await page.getByRole("button", { name: "Undo" }).click();
  await expect.poll(() => existsSync(mainPath)).toBe(true);

  await page.locator(".code-tree-label", { hasText: "main.go" }).click();
  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+End");
  await page.keyboard.press("Enter");
  await page.keyboard.type("// recovered hot exit");
  await page.waitForTimeout(900);
  await page.reload();
  await expect(page.getByRole("tab", { name: /main\.go/ })).toBeVisible();
  await expect(page.locator(".view-lines")).toContainText("recovered hot exit");

  writeFileSync(mainPath, "package main\n\n// conflicting disk edit\nfunc main() {}\n", "utf8");
  await expect(page.locator(".code-tab-conflict[title='Changed on disk']")).toBeVisible();
  await page.keyboard.press("Control+s");
  await page.getByRole("button", { name: "Compare" }).click();
  await expect(page.locator(".code-diff-dialog")).toBeVisible();
  await page.locator(".code-diff-dialog").getByRole("button", { name: "Close" }).click();
  await page.getByRole("button", { name: "Reload from Disk" }).click();
  await expect(page.locator(".view-lines")).toContainText("conflicting disk edit");

  await page.getByRole("button", { name: "Settings" }).click();
  await expect(page.locator(".settings-view")).toBeVisible();
  await page.getByRole("button", { name: "Back to previous view" }).click();
  await expect(page.locator(".code-app-shell")).toBeVisible();
  await page.waitForTimeout(500);
  await expect(page.locator(".code-app-shell")).toBeVisible();

  // Reloading at the Settings hash creates a true direct-load scenario with
  // no in-memory origin. Back must use Chat as the safe fallback.
  await page.goto("/#/settings");
  await page.reload();
  await expect(page.locator(".settings-view")).toBeVisible();
  await page.getByRole("button", { name: "Back to previous view" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
});
