import { expect, test } from "@playwright/test";
import { mkdirSync, readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

test("Ctrl+C copies terminal selections, clears the highlight, and otherwise sends an interrupt", async ({ page, context }) => {
  const directory = dirname(fileURLToPath(import.meta.url));
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  const workspace = resolve(dirname(state.workspace), "terminal-clipboard");
  mkdirSync(workspace, { recursive: true });
  await context.grantPermissions(["clipboard-read", "clipboard-write"]);
  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const setup = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (setup) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Playwright Terminal Clipboard");
  await page.getByRole("button", { name: setup ? "Finish setup" : "Sign in" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  await page.evaluate(async (mainPath) => {
    const request = async (path: string, method: string, body: unknown) => {
      const response = await fetch(path, { method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      const result = await response.json();
      if (!response.ok) throw new Error(JSON.stringify(result));
      return result.data;
    };
    const created = await request("/api/workspaces", "POST", { name: "Terminal Clipboard", mainPath, folders: [] });
    await request("/api/workspaces/active", "PUT", { id: created.workspace.id });
  }, workspace);
  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await page.getByRole("button", { name: "Open terminal", exact: true }).click();
  await expect(page.locator(".terminal-status-text")).toHaveText("Running");
  const input = page.locator(".terminal-xterm-instance .xterm-helper-textarea");
  const selection = page.locator(".terminal-xterm-instance .xterm-selection div");
  const terminalInput: string[] = [];
  // Shells can enable focus reporting; the clipboard fallback briefly blurs xterm.
  const typedInput = () => terminalInput.join("").replace(/\u001b\[[IO]/g, "");
  page.on("request", (request) => {
    if (/\/terminal\/sessions\/[^/]+\/input$/.test(request.url())) terminalInput.push(request.postDataJSON().data);
  });
  await input.focus();
  await page.keyboard.type("echo ECHO_TERMINAL_COPY_OK");
  await page.keyboard.press("Enter");
  const output = page.locator(".terminal-xterm-instance .xterm-rows").getByText("ECHO_TERMINAL_COPY_OK", { exact: true }).last();
  await expect(output).toBeVisible();
  await expect.poll(() => terminalInput.join("")).toContain("echo ECHO_TERMINAL_COPY_OK\r");

  for (const fallback of [false, true]) {
    await page.evaluate(() => navigator.clipboard.writeText("before terminal copy"));
    if (fallback) await page.evaluate(() => Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined }));
    // xterm receives mouse input on its screen overlay above the text spans.
    const bounds = await output.boundingBox();
    expect(bounds).not.toBeNull();
    await page.mouse.dblclick(bounds!.x + bounds!.width / 2, bounds!.y + bounds!.height / 2);
    await expect(selection).not.toHaveCount(0);
    terminalInput.length = 0;
    await page.keyboard.press("Control+c");
    await expect(selection).toHaveCount(0);
    await expect(input).toBeFocused();
    if (fallback) await page.evaluate(() => Reflect.deleteProperty(navigator, "clipboard"));
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe("ECHO_TERMINAL_COPY_OK");
    expect(typedInput()).toBe("");
    await page.keyboard.press("Control+c");
    await expect.poll(typedInput).toBe("\u0003");
  }
});
