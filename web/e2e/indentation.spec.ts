import { expect, test } from "@playwright/test";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

test("indentation defaults, detection, selector, and persistence in real Monaco", async ({ page }, testInfo) => {
  test.setTimeout(120_000);
  const directory = dirname(fileURLToPath(import.meta.url));
  const state = JSON.parse(readFileSync(join(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  const workspace = join(dirname(state.workspace), "indentation-workspace");
  mkdirSync(workspace, { recursive: true });
  const spaced = "one\n  two\n    three\n  four\n";
  writeFileSync(join(workspace, "blank.txt"), "");
  writeFileSync(join(workspace, "spaces.txt"), spaced);
  const git = (...args: string[]) => execFileSync("git", ["-C", workspace, ...args]);
  git("init", "-b", "main");
  git("add", ".");
  git("-c", "user.name=Echo E2E", "-c", "user.email=echo-e2e@example.com", "commit", "-m", "Indentation fixture");

  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const setup = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (setup) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Playwright Indentation");
  await page.getByRole("button", { name: setup ? "Finish setup" : "Sign in" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  const workspaceId = await page.evaluate(async (mainPath) => {
    const request = async (path: string, body: unknown, method = "PUT") => {
      const response = await fetch(path, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      const result = await response.json();
      if (!response.ok) throw new Error(JSON.stringify(result));
      return result.data;
    };
    const data = await request("/api/workspaces", { name: "Indentation", mainPath, folders: [] }, "POST");
    await request("/api/workspaces/active", { id: data.workspace.id });
    const settings = (await (await fetch("/api/settings")).json()).data.settings;
    delete settings.editorInsertSpaces;
    delete settings.editorTabSize;
    await request("/api/settings", { settings });
    return data.workspace.id as string;
  }, workspace);
  await page.goto("/#/code");
  const shell = page.locator(".code-app-shell");
  await expect(shell).toHaveAttribute("aria-busy", "false");
  const status = page.locator('[data-status="indentation"]');
  const input = page.locator("[data-monaco-host]").getByRole("textbox", { name: "Editor content" });
  const open = async (name: string) => {
    await page.keyboard.press("Control+p");
    await page.getByLabel("Go to File").fill(name);
    await page.getByRole("option", { name: new RegExp(name.replace(".", "\\.")) }).first().click();
    await expect(page.locator(".code-tab.is-active")).toContainText(name);
  };
  const preferences = () => page.evaluate(async () => (await (await fetch("/api/settings")).json()).data.settings);
  const setIndentation = async (style: string, width: string) => {
    await status.click();
    await page.getByRole("dialog", { name: "Indentation" }).getByLabel("Indent using").selectOption(style);
    await page.getByRole("dialog", { name: "Indentation" }).getByLabel("Width").selectOption(width);
    await page.keyboard.press("Escape");
    await expect(status).toBeFocused();
    await expect.poll(async () => {
      const settings = await preferences();
      return [settings.editorInsertSpaces, settings.editorTabSize];
    }).toEqual([style === "spaces", Number(width)]);
  };

  await expect(status).toHaveText("Tabs: 4");
  await open("blank.txt");
  await expect(status).toHaveText("Tabs: 4");
  await input.focus();
  await page.keyboard.press("Tab");
  await page.keyboard.insertText("tabbed");
  await page.keyboard.press("Control+s");
  await expect.poll(() => readFileSync(join(workspace, "blank.txt"), "utf8")).toBe("\ttabbed");

  await open("spaces.txt");
  await expect(status).toHaveText("Spaces: 2");
  await status.focus();
  await page.keyboard.press("Enter");
  await expect(page.getByRole("dialog", { name: "Indentation" })).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("indentation-selector.png") });
  await page.keyboard.press("Escape");
  await setIndentation("tabs", "4");
  await expect(status).toHaveText("Tabs: 4");
  await expect(page.locator(".code-tab.is-active .code-tab-dirty")).not.toHaveClass(/is-visible/);
  expect(readFileSync(join(workspace, "spaces.txt"), "utf8")).toBe(spaced);
  await setIndentation("spaces", "3");
  await input.focus();
  await page.keyboard.press("Control+Home");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Control+s");
  await expect.poll(() => readFileSync(join(workspace, "spaces.txt"), "utf8")).toBe("   " + spaced);

  await open("blank.txt");
  await expect(status).toHaveText("Tabs: 4");
  await page.keyboard.press("Control+n");
  await expect(page.locator(".code-tab.is-active")).toContainText("Untitled-");
  await expect(status).toHaveText("Spaces: 3");
  await setIndentation("spaces", "2");
  // Font-size saves and indentation saves share the settings writer.
  await input.focus();
  await page.keyboard.down("Control");
  await page.locator("[data-monaco-host]").hover();
  await page.mouse.wheel(0, -100);
  await page.keyboard.up("Control");
  await expect.poll(async () => (await preferences()).editorFontSize).toBeGreaterThan(13.5);
  await expect.poll(async () => (await preferences()).editorTabSize).toBe(2);
  await page.reload();
  await expect(shell).toHaveAttribute("aria-busy", "false");
  await page.keyboard.press("Control+n");
  await expect(status).toHaveText("Spaces: 2");

  // Reopening detects file contents again, instead of persisting the override.
  await open("spaces.txt");
  await expect(status).toHaveText("Spaces: 2");
  await page.getByRole("button", { name: "Settings", exact: true }).click();
  await expect(page.locator(".settings-view")).toBeVisible();
  await page.getByLabel("Settings sections").getByRole("button", { name: "Source Control", exact: true }).click();
  await page.getByLabel("Split diff view").uncheck();
  await expect.poll(async () => (await preferences()).disableSourceControlSplitDiffView).toBe(true);
  expect((await preferences()).editorInsertSpaces).toBe(true);
  expect((await preferences()).editorTabSize).toBe(2);
  await page.getByRole("button", { name: "Back to previous view" }).click();
  await expect(shell).toHaveAttribute("aria-busy", "false");

  // Working-tree diffs expose the same preference controls and model options.
  await page.getByRole("button", { name: "Source Control", exact: true }).click();
  const changes = page.locator(".git-change-group[data-git-group='unstaged']");
  await changes.locator(".git-change-row", { hasText: "spaces.txt" }).click();
  await expect(page.locator("[data-monaco-diff-host]")).toBeVisible();
  await setIndentation("tabs", "4");
  await page.locator("[data-monaco-diff-host] .modified-in-monaco-diff-editor .view-lines[data-mprt]").click();
  await page.keyboard.press("Control+Home");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Control+s");
  await expect.poll(() => readFileSync(join(workspace, "spaces.txt"), "utf8")).toBe("\t   " + spaced);

  // Workspace switches use the saved default for empty new buffers.
  await page.evaluate(async ({ path }) => {
    const created = await (await fetch("/api/workspaces", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: "Indentation secondary", mainPath: path, folders: [] }) })).json();
    await fetch("/api/workspaces/active", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: created.data.workspace.id }) });
  }, { path: state.secondaryWorkspace });
  await page.reload();
  await expect(shell).toHaveAttribute("aria-busy", "false");
  await expect(status).toHaveText("Tabs: 4");
  await page.keyboard.press("Control+n");
  await expect(status).toHaveText("Tabs: 4");
  await page.evaluate(async (id) => { await fetch("/api/workspaces/active", { method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id }) }); }, workspaceId);
});
