import { expect, test } from "@playwright/test";
import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createServer, connect } from "node:net";

test("P4 changelists use the shared Monaco editor and checkout-aware save", async ({ page }, testInfo) => {
  test.setTimeout(180_000);
  const p4d = process.env.ECHO_P4D || "p4d";
  try { execFileSync(p4d, ["-V"], { windowsHide: true, stdio: "ignore" }); execFileSync("p4", ["-V"], { windowsHide: true, stdio: "ignore" }); }
  catch (error) { if (process.env.ECHO_REQUIRE_P4 === "1") throw error; test.skip(true, "Requires isolated p4 and p4d binaries"); }
  const state = JSON.parse(readFileSync(join(dirname(fileURLToPath(import.meta.url)), "../test-results/e2e-runtime/state.json"), "utf8"));
  const directory = join(dirname(state.workspace), "p4-browser");
  const workspace = join(directory, "client"); const depot = join(directory, "server");
  mkdirSync(workspace, { recursive: true }); mkdirSync(depot, { recursive: true });
  const listener = createServer(); await new Promise<void>((resolve) => listener.listen(0, "127.0.0.1", resolve));
  const address = listener.address(); if (!address || typeof address === "string") throw new Error("No test port");
  const port = address.port; await new Promise<void>((resolve, reject) => listener.close((err) => err ? reject(err) : resolve()));
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.toUpperCase().startsWith("P4")));
  Object.assign(env, { P4PORT: `127.0.0.1:${port}`, P4CLIENT: "echo-browser", P4USER: "echo-test", P4CHARSET: "utf8", P4CONFIG: ".unused-test-config", P4ENVIRO: join(directory, "env"), P4TICKETS: join(directory, "tickets"), P4TRUST: join(directory, "trust") });
  execFileSync(p4d, ["-r", depot, "-xi"], { cwd: depot, env, windowsHide: true });
  const server = spawn(p4d, ["-r", depot, "-p", `127.0.0.1:${port}`, "-L", join(directory, "p4.log"), "-J", join(directory, "journal")], { cwd: depot, env, windowsHide: true, stdio: "ignore" });
  const p4 = (args: string[], input?: string) => execFileSync("p4", args, { cwd: workspace, env, windowsHide: true, encoding: "utf8", input });
  try {
    await expect.poll(async () => await new Promise<boolean>((resolve) => { const socket = connect(port, "127.0.0.1"); socket.once("connect", () => { socket.destroy(); resolve(true); }); socket.once("error", () => resolve(false)); })).toBe(true);
    p4(["client", "-i"], `Client: echo-browser\nOwner: echo-test\nRoot: ${workspace}\nLineEnd: unix\nView:\n\t//depot/... //echo-browser/...\n`);
    const file = join(workspace, "blocks.txt");
    const base = "first\n" + Array.from({ length: 12 }, (_, i) => `context ${i}\n`).join("") + "last\n";
    const working = base.replace("first", "FIRST").replace("last", "LAST");
    writeFileSync(file, base); p4(["add", "blocks.txt"]); p4(["submit", "-d", "seed browser fixture"]); p4(["edit", "blocks.txt"]); writeFileSync(file, working);
    await page.goto("/");
    await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
    const setup = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
    if (setup) { await page.getByLabel("Setup code").fill(state.setupCode); await page.getByLabel("Confirm password").fill("Echo-E2E-Password!"); }
    await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!"); await page.getByLabel("Device name").fill("P4 Browser Test");
    await page.getByRole("button", { name: setup ? "Finish setup" : "Sign in" }).click(); await expect(page.locator(".app-shell")).toBeVisible();
    await page.evaluate(async ({ workspace, port }) => {
      const request = async (path: string, body: unknown, method = "PUT") => { const response = await fetch(path, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) }); const result = await response.json(); if (!response.ok) throw new Error(JSON.stringify(result)); return result.data; };
      const created = await request("/api/workspaces", { name: "P4 Browser", mainPath: workspace, folders: [] }, "POST"); const id = created.workspace.id;
      await request("/api/workspaces/active", { id });
      const roots = (await (await fetch(`/api/workspaces/${id}/fs/roots`)).json()).data.roots;
      await request(`/api/workspaces/${id}/source-control/settings`, { p4: { roots: { [roots[0].id]: { server: `127.0.0.1:${port}`, user: "echo-test", client: "echo-browser" } }, repositories: {} } });
      const repositories = (await (await fetch(`/api/workspaces/${id}/source-control/repositories`)).json()).data.repositories;
      const repo = repositories.find((item: { providerId: string }) => item.providerId === "p4");
      await request(`/api/workspaces/${id}/source-control/repositories/${repo.id}/actions`, { requestId: "enable-test", action: "set_tracking", confirmed: true }, "POST");
      const settings = (await (await fetch("/api/settings")).json()).data.settings;
      await request("/api/settings", { settings: { ...settings, disableSourceControlSplitDiffView: false } });
    }, { workspace, port });
    await page.goto("/#/code?sidebar=source-control");
    await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
    const defaultToggle = page.locator("[data-git-group-toggle='default']");
    const localToggle = page.locator("[data-git-group-toggle='local']");
    await expect(defaultToggle).toHaveAttribute("aria-expanded", "false");
    await expect(localToggle).toHaveAttribute("aria-expanded", "false");
    const row = page.locator("[data-git-group-id='default'] .git-change-row", { hasText: "blocks.txt" });
    await expect(row).toBeHidden();
    await defaultToggle.click();
    await expect(row).toBeVisible();
    await defaultToggle.locator(".codicon").click();
    await expect(defaultToggle).toHaveAttribute("aria-expanded", "false");
    await expect(row).toBeHidden();
    await defaultToggle.click();
    await row.dblclick();
    const reverts = page.getByRole("button", { name: "Revert Change", exact: true });
    await expect(reverts).toHaveCount(2);
    await expect(page.getByRole("button", { name: /^(Stage|Protect|Unstage|Unprotect) Change$/ })).toHaveCount(0);
    await page.screenshot({ path: testInfo.outputPath("p4-split.png") });
    await reverts.first().click(); await expect(reverts).toHaveCount(1); expect(readFileSync(file, "utf8")).toBe(working);
    await page.keyboard.press("Control+z"); await expect(reverts).toHaveCount(2);
    await page.keyboard.press("Control+y"); await expect(reverts).toHaveCount(1);
    await page.keyboard.press("Control+s"); await expect.poll(() => readFileSync(file, "utf8")).toBe(base.replace("last", "LAST"));
    await page.getByRole("button", { name: "Use Inline Diff" }).click(); await expect(page.locator("[data-monaco-diff-host]")).toHaveAttribute("data-diff-layout", "inline");
    await expect(reverts).toHaveCount(1); await page.screenshot({ path: testInfo.outputPath("p4-inline.png") });
    await page.getByRole("button", { name: "New changelist", exact: true }).click();
    await page.getByLabel("Description", { exact: true }).fill("Unshelved browser changelist");
    await page.getByRole("button", { name: "Create", exact: true }).click();
    await expect(page.locator("[data-p4-active]")).not.toHaveValue("default");
    const active = await page.locator("[data-p4-active]").inputValue();
    const activeToggle = page.locator(`[data-git-group-toggle='${active}']`);
    await expect(activeToggle).toHaveAttribute("aria-expanded", "false");
    await activeToggle.click();
    await expect(page.locator(".p4-description")).toContainText("Unshelved browser changelist"); await expect(row).toBeVisible();
    await page.reload(); await expect(page.locator(".code-tab.is-active")).toContainText("blocks.txt");
    await expect(page.locator("[data-monaco-diff-host]")).toBeVisible();
    await expect(page.locator("[data-git-group-toggle][aria-expanded='false']")).toHaveCount(3);
    await activeToggle.click();

    // P4V-style metadata changes have no filesystem event; the five-second
    // source-control subscription must still update native group membership.
    p4(["reopen", "-c", active, "blocks.txt"]);
    await expect(page.locator(`[data-git-group-id='${active}'] .git-change-row`, { hasText: "blocks.txt" })).toBeVisible({ timeout: 15_000 });
    await expect(defaultToggle).toHaveAttribute("aria-expanded", "false");
    await expect(localToggle).toHaveAttribute("aria-expanded", "false");
    await localToggle.click();
    writeFileSync(join(workspace, "terminal.txt"), "external work\n");
    await expect(page.locator("[data-git-group-id='local'] .git-change-row", { hasText: "terminal.txt" })).toBeVisible({ timeout: 15_000 });
    await page.getByRole("button", { name: "P4 actions…", exact: true }).click();
    await page.getByRole("menuitem", { name: "Reconcile Echo Changes…", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Review P4 reconciliation" })).toBeVisible();
    await expect(page.getByRole("dialog")).toContainText("terminal.txt");
    await page.getByRole("button", { name: "Apply Reviewed Changes", exact: true }).click();
    await expect(page.locator(`[data-git-group-id='${active}'] .git-change-row`, { hasText: "terminal.txt" })).toBeVisible();
    await expect(defaultToggle).toHaveAttribute("aria-expanded", "false");

    // A later disk write cannot be overwritten by a stale diff buffer.
    await page.locator(`[data-git-group-id='${active}'] .git-change-row`, { hasText: "blocks.txt" }).dblclick();
    await expect(reverts).toHaveCount(1);
    await reverts.first().click();
    writeFileSync(file, "external version\n");
    await page.keyboard.press("Control+s");
    await expect(page.getByRole("heading", { name: /blocks\.txt.*changed on disk/ })).toBeVisible();
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    expect(readFileSync(file, "utf8")).toBe("external version\n");
    await expect(page.locator(".code-tab.is-active [aria-label='Unsaved changes']")).toBeVisible();
  } finally { server.kill(); await new Promise<void>((resolve) => { if (server.exitCode !== null) resolve(); else server.once("exit", () => resolve()); }); }
});
