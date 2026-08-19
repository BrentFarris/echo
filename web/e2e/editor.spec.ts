import { expect, test } from "@playwright/test";
import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));
const password = "Echo-E2E-Password!";

test("first-run auth and the real Monaco filesystem workflow", async ({ page }) => {
  test.setTimeout(300_000);
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8")) as {
    setupCode: string;
    workspace: string;
    secondaryWorkspace: string;
  };
  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Secure this Echo server" })).toBeVisible();
  await page.getByLabel("Setup code").fill(state.setupCode);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByLabel("Confirm password").fill(password);
  await page.getByLabel("Device name").fill("Playwright Chromium");
  await page.getByRole("button", { name: "Finish setup" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();

  const workspaceIDs = await page.evaluate(async (workspacePaths) => {
    const request = async (path: string, init: RequestInit) => {
      const response = await fetch(path, { ...init, headers: { "Content-Type": "application/json" } });
      const payload = await response.json();
      if (!response.ok || payload.ok === false) throw new Error(payload.error || `HTTP ${response.status}`);
      return payload.data;
    };
    const created = await request("/api/workspaces", {
      method: "POST",
      body: JSON.stringify({ name: "E2E Workspace", mainPath: workspacePaths.primary, folders: [] }),
    });
    const secondary = await request("/api/workspaces", {
      method: "POST",
      body: JSON.stringify({ name: "E2E Secondary", mainPath: workspacePaths.secondary, folders: [] }),
    });
    await request("/api/workspaces/active", {
      method: "PUT",
      body: JSON.stringify({ id: created.workspace.id }),
    });
    return { primary: created.workspace.id as string, secondary: secondary.workspace.id as string };
  }, { primary: state.workspace, secondary: state.secondaryWorkspace });
  expect(workspaceIDs.primary).toBeTruthy();
  expect(workspaceIDs.secondary).toBeTruthy();
  await page.reload();
  await expect(page.locator(".app-shell")).toBeVisible();

  // Chat @ references use the workspace index, render as compact rich chips,
  // and open files directly in Echo Code.
  const composer = page.locator("[data-chat-input]");
  await composer.fill("Review @main");
  const mention = page.getByRole("option", { name: /main\.go/ });
  await expect(mention).toBeVisible();
  await mention.click();
  const mentionChip = composer.locator("[data-chat-file-mention]");
  await expect(mentionChip).toHaveAttribute("data-reference-path", /main\.go$/);
  await mentionChip.click();
  await expect(page).toHaveURL(/#\/code$/);
  await expect(page.getByRole("tab", { name: /main\.go/ })).toBeVisible();
  await page.getByRole("button", { name: "Close main.go" }).click();
  await page.getByRole("button", { name: "Chat" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();

  // The terminal is a single workspace session shared across Chat and Code.
  await page.getByRole("button", { name: "Open terminal" }).click();
  await expect(page.locator(".terminal-dock")).toHaveClass(/is-open/);
  await expect(page.locator(".terminal-status-text")).toHaveText("Running");
  await page.locator(".terminal-xterm-instance .xterm-helper-textarea").focus();
  await page.keyboard.type("echo ECHO_TERMINAL_ROUTE_OK");
  await page.keyboard.press("Enter");
  await expect(page.locator(".terminal-xterm-instance .xterm-rows")).toContainText("ECHO_TERMINAL_ROUTE_OK");

  await page.getByRole("button", { name: "Source Control" }).click();
  await expect(page.locator(".code-app-shell")).toBeVisible();
  await expect(page).toHaveURL(/#\/code\?sidebar=git$/);
  await expect(page.getByText("SOURCE CONTROL", { exact: true })).toBeVisible();
  await expect(page.locator(".terminal-xterm-instance .xterm-rows")).toContainText("ECHO_TERMINAL_ROUTE_OK");

  await page.getByRole("button", { name: "Restart terminal" }).click();
  await expect(page.locator(".terminal-status-text")).toHaveText("Running");
  await page.getByRole("button", { name: "Saved commands" }).click();
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await page.getByLabel("Command name").fill("E2E Echo");
  await page.getByLabel("Command text").fill("echo ECHO_TERMINAL_SAVED_OK");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.locator(".code-toast", { hasText: "Command saved" })).toBeVisible();
  await expect(page.locator(".code-toast", { hasText: "terminal session was not found" })).toHaveCount(0);
  await page.getByRole("button", { name: "Saved commands" }).click();
  await page.getByRole("menuitem", { name: /E2E Echo/ }).click();
  await expect(page.locator(".terminal-xterm-instance .xterm-rows")).toContainText("ECHO_TERMINAL_SAVED_OK");

  await page.getByRole("button", { name: "Kill terminal" }).click();
  await expect(page.locator(".terminal-process-message")).toContainText("Process exited");
  await page.locator(".terminal-process-message").getByRole("button", { name: "Restart", exact: true }).click();
  await expect(page.locator(".terminal-status-text")).toHaveText("Running");
  await page.getByRole("button", { name: "Close terminal" }).click();
  await expect(page.locator(".terminal-dock")).not.toHaveClass(/is-open/);

  await page.getByRole("button", { name: "Explorer", exact: true }).click();
  await expect(page).toHaveURL(/#\/code$/);
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
  const mainGoResult = page.getByRole("option", { name: /main\.go/ });
  await expect(mainGoResult).toBeVisible();
  await mainGoResult.click();
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

  // Source Control keeps the editor alive, previews full-file diffs, applies
  // row actions to the current multi-selection, and uses recoverable Trash for
  // untracked reverts.
  const trashPath = join(state.workspace, "trash-me.txt");
  writeFileSync(trashPath, "recoverable\n", "utf8");
  await page.getByRole("button", { name: "Source Control" }).click();
  await expect(page.getByText("SOURCE CONTROL", { exact: true })).toBeVisible();
  const changes = page.locator(".git-change-group[data-git-group='unstaged']");
  const trashRow = changes.locator(".git-change-row", { hasText: "trash-me.txt" });
  await expect(trashRow).toBeVisible();
  await trashRow.hover();
  await trashRow.getByRole("button", { name: "Revert Changes" }).click();
  await page.getByRole("button", { name: "Revert", exact: true }).click();
  await expect.poll(() => existsSync(trashPath)).toBe(false);
  await page.getByRole("button", { name: "Undo" }).click();
  await expect.poll(() => existsSync(trashPath)).toBe(true);

  const mainChange = changes.locator(".git-change-row", { hasText: "main.go" });
  await expect(mainChange).toBeVisible();
  await mainChange.click();
  await expect(page.getByRole("tab", { name: /main\.go \(Working Tree\)/ })).toBeVisible();
  await expect(page.locator("[data-monaco-diff-host]")).toBeVisible();
  await expect(page.getByRole("button", { name: "Previous Change" })).toBeVisible();
  await trashRow.click({ modifiers: ["Control"] });
  await mainChange.hover();
  await mainChange.getByRole("button", { name: "Stage Changes" }).click();

  const staged = page.locator(".git-change-group[data-git-group='staged']");
  const stagedMain = staged.locator(".git-change-row", { hasText: "main.go" });
  const stagedTrash = staged.locator(".git-change-row", { hasText: "trash-me.txt" });
  await expect(stagedMain).toBeVisible();
  await expect(stagedTrash).toBeVisible();
  await stagedMain.click();
  await stagedTrash.click({ modifiers: ["Control"] });
  await stagedTrash.hover();
  await stagedTrash.getByRole("button", { name: "Unstage Changes" }).click();
  await expect(changes.locator(".git-change-row", { hasText: "main.go" })).toBeVisible();
  await changes.getByRole("button", { name: "Stage All Changes" }).click();
  await expect(staged.locator(".git-change-row", { hasText: "main.go" })).toBeVisible();
  await page.getByLabel("Commit message").fill("Commit from Source Control");
  await page.locator(".git-commit-button").click();
  await expect(page.getByText("No changes", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Settings" }).click();
  await expect(page.locator(".settings-view")).toBeVisible();
  await page.getByRole("button", { name: "Git", exact: true }).click();
  const splitDiff = page.getByLabel("Split Git diff view");
  await expect(splitDiff).toBeChecked();
  await splitDiff.uncheck();
  await page.waitForTimeout(200);
  await page.getByRole("button", { name: "Back to previous view" }).click();
  await expect(page.locator(".code-app-shell")).toBeVisible();
  writeFileSync(mainPath, "package main\n\n// inline diff setting\nfunc main() {}\n", "utf8");
  await page.getByRole("button", { name: "Source Control" }).click();
  const inlineMain = page.locator(".git-change-group[data-git-group='unstaged'] .git-change-row", { hasText: "main.go" });
  await expect(inlineMain).toBeVisible();
  await inlineMain.click();
  await expect(page.locator("[data-monaco-diff-host]")).toHaveAttribute("data-diff-layout", "inline");
  await page.getByRole("button", { name: "Use Side-by-Side Diff" }).click();
  await expect(page.locator("[data-monaco-diff-host]")).toHaveAttribute("data-diff-layout", "split");
  await page.waitForTimeout(500);
  await expect(page.locator(".code-app-shell")).toBeVisible();

  // Reloading at the Settings hash creates a true direct-load scenario with
  // no in-memory origin. Back must use Chat as the safe fallback.
  await page.goto("/#/settings");
  await page.reload();
  await expect(page.locator(".settings-view")).toBeVisible();
  await page.getByRole("button", { name: "Back to previous view" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();

  // The mobile shell preserves the same workspace and route capabilities as
  // the desktop rail without covering each view's bottom-most controls.
  await page.setViewportSize({ width: 390, height: 844 });
  let mobileNav = page.locator("[data-mobile-primary-nav]");
  await expect(mobileNav).toBeVisible();
  await expect(page.locator(".left-nav")).toBeHidden();
  const touchTargets = await mobileNav.locator(".mobile-nav-pill, .mobile-nav-tab").evaluateAll((items) => (
    items.map((item) => ({ width: item.getBoundingClientRect().width, height: item.getBoundingClientRect().height }))
  ));
  expect(touchTargets.every((target) => target.width >= 44 && target.height >= 44)).toBe(true);

  const chatWorkspace = mobileNav.locator(".workspace-dropdown-trigger");
  await chatWorkspace.click();
  await expect(page.locator(".workspace-dropdown-anchor")).toHaveAttribute("data-placement", "above");
  await page.getByRole("menuitem", { name: /E2E Secondary/ }).click();
  await expect(chatWorkspace.locator("[data-mobile-workspace-name]")).toHaveText("E2E Secondary");
  await expect(page.getByLabel("Message Echo")).toBeVisible();

  await mobileNav.getByRole("button", { name: "Code", exact: true }).click();
  await expect(page.locator(".code-app-shell")).toBeVisible();
  mobileNav = page.locator("[data-mobile-primary-nav]");
  await expect(mobileNav.getByRole("button", { name: "Code", exact: true })).toHaveAttribute("aria-current", "page");
  await mobileNav.getByRole("button", { name: "Code", exact: true }).click();
  await expect(page.locator(".code-app-shell")).toHaveClass(/is-explorer-open/);
  await expect(page.locator(".code-tree-label", { hasText: "secondary.txt" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(page.locator(".code-app-shell")).not.toHaveClass(/is-explorer-open/);

  await mobileNav.getByRole("button", { name: "Source Control" }).click();
  await expect(page).toHaveURL(/#\/code\?sidebar=git$/);
  await expect(page.locator(".code-app-shell")).toHaveClass(/is-explorer-open/);
  await expect(page.getByText("SOURCE CONTROL", { exact: true })).toBeVisible();
  await expect(mobileNav.getByRole("button", { name: "Source Control" })).toHaveAttribute("aria-current", "page");

  const codeWorkspace = mobileNav.locator(".workspace-dropdown-trigger");
  await codeWorkspace.click();
  await page.getByRole("menuitem", { name: /E2E Workspace/ }).click();
  await expect(page).toHaveURL(/#\/code\?sidebar=git$/);
  await expect(page.locator("[data-mobile-workspace-name]")).toHaveText("E2E Workspace");

  mobileNav = page.locator("[data-mobile-primary-nav]");
  await mobileNav.getByRole("button", { name: "Settings" }).click();
  await expect(page.locator(".settings-view")).toBeVisible();
  mobileNav = page.locator("[data-mobile-primary-nav]");
  await expect(mobileNav.getByRole("button", { name: "Settings" })).toHaveAttribute("aria-current", "page");
  await page.getByRole("button", { name: "Git", exact: true }).click();
  await expect(page.getByLabel("Split Git diff view")).toBeVisible();

  await mobileNav.getByRole("button", { name: "Chat" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  await page.setViewportSize({ width: 320, height: 700 });
  mobileNav = page.locator("[data-mobile-primary-nav]");
  await page.getByRole("button", { name: "Open terminal" }).click();
  await expect(page.locator(".terminal-dock")).toHaveClass(/is-open/);
  await expect(page.locator(".terminal-status-text")).toHaveText("Running");
  const mobileTerminal = await page.locator(".terminal-dock").evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return { position: getComputedStyle(element).position, top: rect.top, width: rect.width, height: rect.height };
  });
  expect(mobileTerminal.position).toBe("fixed");
  expect(mobileTerminal.top).toBeLessThanOrEqual(1);
  expect(mobileTerminal.width).toBeGreaterThanOrEqual(319);
  expect(mobileTerminal.height).toBeGreaterThanOrEqual(699);
  await page.getByRole("button", { name: "Close terminal" }).click();
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  const layout = await page.evaluate(() => {
    const nav = document.querySelector("[data-mobile-primary-nav]")?.getBoundingClientRect();
    const terminal = document.querySelector(".terminal-dock")?.getBoundingClientRect();
    const composer = document.querySelector(".chat-composer")?.getBoundingClientRect();
    return nav && terminal && composer
      ? { navTop: nav.top, terminalBottom: terminal.bottom, terminalTop: terminal.top, composerBottom: composer.bottom }
      : null;
  });
  expect(layout).not.toBeNull();
  expect(layout!.navTop).toBeGreaterThanOrEqual(layout!.terminalBottom - 1);
  expect(layout!.terminalTop).toBeGreaterThanOrEqual(layout!.composerBottom - 1);

  const finalWorkspace = mobileNav.locator(".workspace-dropdown-trigger");
  await finalWorkspace.click();
  await page.keyboard.press("Escape");
  await expect(page.locator(".workspace-dropdown-anchor")).toHaveCount(0);
  await expect(finalWorkspace).toBeFocused();
});
