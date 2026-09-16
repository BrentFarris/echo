import { expect, test } from "@playwright/test";
import { mkdirSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

for (const provider of ["git", "fossil"] as const) {
  test(`${provider} change-block controls in real Monaco`, async ({ page }, testInfo) => {
    test.setTimeout(180_000);
    const directory = dirname(fileURLToPath(import.meta.url));
    const state = JSON.parse(readFileSync(join(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
    const workspace = join(dirname(state.workspace), `hunks-${provider}`);
    mkdirSync(workspace, { recursive: true });
    const file = join(workspace, "blocks.txt");
    const base = "first\n" + Array.from({ length: 12 }, (_, i) => `context ${i}\n`).join("") + "last\n";
    const working = base.replace("first", "FIRST").replace("last", "LAST");
    const cases: Array<{ name: string; before: string | null; after: string | null }> = [
      { name: "newline.txt", before: "one", after: "one\n" },
      { name: "insert.txt", before: "a\nb\n", after: "new\na\nb\n" },
      { name: "delete-end.txt", before: "a\nb", after: "a" },
      { name: "empty.txt", before: "", after: "new" },
      { name: "new.txt", before: null, after: "new" },
      { name: "deleted.txt", before: "removed\n", after: null },
    ];
    writeFileSync(file, base);
    for (const item of cases) if (item.before !== null) writeFileSync(join(workspace, item.name), item.before);
    const git = (...args: string[]) => execFileSync("git", ["-C", workspace, ...args], { encoding: "utf8", windowsHide: true });
    const fossil = (...args: string[]) => execFileSync("fossil", args, { cwd: workspace, encoding: "utf8", windowsHide: true });
    if (provider === "git") {
      git("init", "-b", "main"); git("config", "core.autocrlf", "false"); git("add", ".");
      git("-c", "user.name=Echo E2E", "-c", "user.email=echo-e2e@example.com", "commit", "-m", "Hunk fixture");
    } else {
      const repository = join(dirname(workspace), "hunks.fossil");
      fossil("init", "--admin-user", "echo-test", repository); fossil("open", repository, "--keep", "--nosync", "--user", "echo-test");
      fossil("user", "default", "echo-test"); fossil("add", "blocks.txt", ...cases.filter((item) => item.before !== null).map((item) => item.name));
      fossil("commit", "--nosync", "--no-prompt", "--no-warnings", "-m", "Hunk fixture");
    }
    writeFileSync(file, working);
    for (const item of cases) {
      if (item.after === null) unlinkSync(join(workspace, item.name));
      else writeFileSync(join(workspace, item.name), item.after);
    }

    await page.goto("/");
    await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
    const setup = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
    if (setup) {
      await page.getByLabel("Setup code").fill(state.setupCode);
      await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
    }
    await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
    await page.getByLabel("Device name").fill("Playwright Diff Hunks");
    await page.getByRole("button", { name: setup ? "Finish setup" : "Sign in" }).click();
    await expect(page.locator(".app-shell")).toBeVisible();
    const workspaceId = await page.evaluate(async (mainPath) => {
      const request = async (path: string, body: unknown, method = "PUT") => {
        const response = await fetch(path, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
        const data = await response.json(); if (!response.ok) throw new Error(JSON.stringify(data)); return data.data;
      };
      const created = await request("/api/workspaces", { name: `Diff Hunks ${mainPath.split(/[\\/]/).pop()}`, mainPath, folders: [] }, "POST");
      await request("/api/workspaces/active", { id: created.workspace.id });
      const settings = (await (await fetch("/api/settings")).json()).data.settings;
      await request("/api/settings", { settings: { ...settings, disableSourceControlSplitDiffView: false } });
      return created.workspace.id as string;
    }, workspace);
    await page.goto("/#/code?sidebar=source-control");
    await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
    const workingGroup = provider === "git" ? "unstaged" : "working";
    const includedGroup = provider === "git" ? "staged" : "protected";
    const row = (group: string) => page.locator(`.git-change-group[data-git-group='${group}'] .git-change-row`, { hasText: "blocks.txt" });
    await row(workingGroup).dblclick();
    const plus = page.getByRole("button", { name: provider === "git" ? "Stage Change" : "Protect Change", exact: true });
    const minus = page.getByRole("button", { name: provider === "git" ? "Unstage Change" : "Unprotect Change", exact: true });
    const arrows = page.getByRole("button", { name: "Revert Change", exact: true });
    await expect(plus).toHaveCount(2);
    await expect(plus.first()).toBeEnabled();
    await expect(arrows).toHaveCount(2);
    const buttonBounds = await plus.first().boundingBox();
    const modifiedBounds = await page.locator("[data-monaco-diff-host] .modified-in-monaco-diff-editor").boundingBox();
    expect(Math.abs(buttonBounds!.x - modifiedBounds!.x)).toBeLessThan(30);
    await page.screenshot({ path: testInfo.outputPath(`${provider}-split.png`) });
    await plus.first().click();
    await expect(plus).toHaveCount(1);
    await expect(row(includedGroup)).toBeVisible();
    if (provider === "git") expect(git("show", ":blocks.txt")).toBe(base.replace("first", "FIRST"));
    expect(readFileSync(file, "utf8")).toBe(working);
    await row(includedGroup).dblclick();
    await expect(minus).toHaveCount(1);
    await expect(arrows).toHaveCount(0);
    await minus.click();
    await expect(minus).toHaveCount(0);
    await expect(page.locator("[data-monaco-diff-host]")).toBeVisible();
    await row(workingGroup).click();
    await expect(plus).toHaveCount(2);
    await expect(arrows.first()).toBeEnabled();
    await arrows.first().click();
    await expect(plus).toHaveCount(1);
    await expect(plus.first()).toBeDisabled();
    await expect(plus.first()).toHaveAttribute("title", /Save this file before/);
    expect(readFileSync(file, "utf8")).toBe(working);
    await page.keyboard.press("Control+z");
    await expect(plus).toHaveCount(2);
    await page.keyboard.press("Control+y");
    await expect(plus).toHaveCount(1);
    await page.keyboard.press("Control+z");
    await page.keyboard.press("Control+s");
    await expect(plus).toHaveCount(2);
    await expect(plus.first()).toBeEnabled();
    await page.getByRole("button", { name: "Use Inline Diff" }).click();
    await expect(page.locator("[data-monaco-diff-host]")).toHaveAttribute("data-diff-layout", "inline");
    await expect(plus.first()).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath(`${provider}-inline.png`) });
    await plus.last().click();
    await expect(plus).toHaveCount(1);
    if (provider === "git") expect(git("show", ":blocks.txt")).toBe(base.replace("last", "LAST"));
    else {
      const frozen = await page.evaluate(async (workspaceId) => {
        const base = `/api/workspaces/${workspaceId}/source-control/repositories`;
        const repositories = (await (await fetch(base)).json()).data.repositories;
        return (await (await fetch(`${base}/${repositories[0].id}/diff?kind=change&groupId=protected&path=blocks.txt`)).json()).data;
      }, workspaceId);
      expect(frozen.modified.content).toBe(base.replace("last", "LAST"));
    }
    expect(readFileSync(file, "utf8")).toBe(working);
    // Exercise actual Monaco mappings for empty sides and EOF. The browser
    // must send the same block the user sees, even without a final newline.
    for (const item of cases) {
      const group = provider === "fossil" && item.before === null ? "untracked" : workingGroup;
      await page.locator(`.git-change-group[data-git-group='${group}'] .git-change-row`, { hasText: item.name }).click();
      await expect(page.locator(".code-tab.is-active")).toContainText(item.name);
      await expect(plus).toHaveCount(1);
      await expect(plus).toBeEnabled();
      await plus.click();
      await expect(plus).toHaveCount(0);
      const snapshot = await page.evaluate(async ({ workspaceId, group, path }) => {
        const base = `/api/workspaces/${workspaceId}/source-control/repositories`;
        const repositories = (await (await fetch(base)).json()).data.repositories;
        return (await (await fetch(`${base}/${repositories[0].id}/diff?kind=change&groupId=${group}&path=${path}`)).json()).data;
      }, { workspaceId, group: includedGroup, path: item.name });
      expect(snapshot.modified.content).toBe(item.after || "");
      expect(snapshot.modified.exists).toBe(item.after !== null);
      await page.locator(`.git-change-group[data-git-group='${includedGroup}'] .git-change-row`, { hasText: item.name }).click();
      await expect(minus).toHaveCount(1);
      await expect(arrows).toHaveCount(0);
      await minus.click();
      await expect(minus).toHaveCount(0);
    }
  });
}
