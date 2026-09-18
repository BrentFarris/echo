import { expect, test, type Page } from "@playwright/test";
import { createServer, type Server, type ServerResponse } from "node:http";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

let provider: Server, port = 0;
const requests: any[] = [];
const held = new Set<ServerResponse>();

test.beforeAll(async () => {
  provider = createServer(async (request, response) => {
    let raw = "";
    for await (const chunk of request) raw += chunk.toString();
    const body = JSON.parse(raw);
    requests.push(body);
    response.writeHead(200, { "Content-Type": "text/event-stream" });
    const event = (delta: unknown, finish_reason: string | null = null) => response.write(`data: ${JSON.stringify({ choices: [{ delta, finish_reason }] })}\n\n`);
    const finish = (reason = "stop") => { event({}, reason); response.end("data: [DONE]\n\n"); };
    const last = body.messages.at(-1);
    if (last.role === "tool") { event({ content: "Preview ready. You can accept it or send a follow-up." }); finish(); return; }
    const prompt = String(last.content);
    if (prompt.includes("hold")) {
      event({ content: "Working on your request…" });
      held.add(response); response.on("close", () => held.delete(response)); return;
    }
    if (prompt.includes("explain")) { event({ content: "The selected code defines a value. The referenced helper provides context." }); finish(); return; }
    const context = body.messages.findLast((message: any) => String(message.content).startsWith("Current editor context (JSON data):\n"));
    if (!context) { event({ content: "Side chat response." }); finish(); return; }
    const editor = JSON.parse(context.content.split("(JSON data):\n")[1]);
    const edit = prompt.includes("follow-up") ? { oldText: "const value = 2;", newText: "const value = 3;" }
      : prompt.includes("write empty") ? { oldText: "", newText: "Hello 😀\n" }
      : prompt.includes("emoji") ? { oldText: "😀", newText: "😃" }
      : prompt.includes("delete all") ? { oldText: editor.content, newText: "" }
      : { oldText: "const value = 1;", newText: "const value = 2;" };
    // Real provider tool-call streaming, through Echo's real inline endpoint.
    const argumentsJSON = JSON.stringify({ edits: [edit] });
    event({ tool_calls: [{ index: 0, id: "inline-edit", type: "function", function: { name: "propose_inline_edits", arguments: argumentsJSON.slice(0, 12) } }] });
    event({ tool_calls: [{ index: 0, function: { arguments: argumentsJSON.slice(12) } }] });
    finish("tool_calls");
  });
  await new Promise<void>(resolve => provider.listen(0, "127.0.0.1", resolve));
  port = (provider.address() as { port: number }).port;
});
test.afterAll(async () => {
  held.forEach(response => response.end());
  provider.closeAllConnections();
  await new Promise<void>(resolve => provider.close(() => resolve()));
});

async function api(page: Page, path: string, body?: unknown, method = body === undefined ? "GET" : "POST") {
  const response = await page.request.fetch(path, { method, data: body });
  const result = await response.json();
  expect(response.ok(), JSON.stringify(result)).toBeTruthy();
  return result.data;
}

test("inline conversations preview, follow up, stop, accept, undo, and survive tab switches", async ({ page }, testInfo) => {
  test.setTimeout(180_000);
  const runtime = JSON.parse(readFileSync(join(dirname(fileURLToPath(import.meta.url)), "../test-results/e2e-runtime/state.json"), "utf8"));
  const workspace = join(dirname(runtime.workspace), "inline-workspace");
  mkdirSync(workspace, { recursive: true });
  const original = "const value = 1;\r\n// Unicode 😀\r\nexport { value };\r\n";
  const mainPath = join(workspace, "main.ts");
  writeFileSync(mainPath, original);
  writeFileSync(join(workspace, "helper.ts"), "export const helper = 'reference-content';\n");
  const errors: string[] = [];
  page.on("pageerror", error => errors.push(error.message));
  await page.goto("/");
  await expect(page.getByRole("heading", { name: /Secure this Echo server|Welcome back/ })).toBeVisible();
  const setup = await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible();
  if (setup) {
    await page.getByLabel("Setup code").fill(runtime.setupCode);
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
  }
  await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
  await page.getByLabel("Device name").fill("Inline Chat Test");
  await page.getByRole("button", { name: setup ? "Finish setup" : "Sign in" }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  const { workspace: created } = await api(page, "/api/workspaces", { name: "Inline", mainPath: workspace, folders: [] });
  await api(page, "/api/workspaces/active", { id: created.id }, "PUT");
  const { settings } = await api(page, "/api/settings");
  settings.endpoints = [{ ...settings.endpoints[0], endpoint: `http://127.0.0.1:${port}/v1`, model: "inline-fake", maxTokens: 1024, contextLength: 65536 }];
  await api(page, "/api/settings", { settings }, "PUT");
  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  const editor = page.locator("[data-monaco-host]").getByRole("textbox", { name: "Editor content" });
  const lines = page.locator("[data-monaco-host] .view-lines");
  const zone = page.locator(".inline-chat-zone");
  const input = zone.getByRole("textbox", { name: "Inline message" });
  const openFile = async (name: string) => {
    await page.keyboard.press("Control+p");
    await page.getByLabel("Go to File").fill(name);
    await page.getByRole("option", { name: new RegExp(name.replace(".", "\\.")) }).first().click();
    await expect(page.locator(".code-tab.is-active")).toContainText(name);
  };
  const openChat = async () => { await editor.focus(); await page.keyboard.press("Control+i"); await expect(input).toBeFocused(); };
  const send = async (message: string) => { await input.fill(message); await input.press("Enter"); await expect(zone).toHaveAttribute("aria-busy", "false"); };
  await openFile("main.ts");
  await editor.focus(); await page.keyboard.press("Control+End"); await page.keyboard.insertText("// unsaved note\n");
  await page.keyboard.press("Control+Home"); await page.keyboard.press("Shift+End");
  await openChat();
  await input.fill("draft"); await page.keyboard.press("Control+i");
  await expect(zone).toHaveCount(1); await expect(input).toHaveText("draft");
  await input.fill("explain @helper");
  await expect(zone.getByRole("option").first()).toBeVisible();
  await input.press("Enter");
  await expect(zone.locator(".chat-mention-chip")).toHaveText("helper.ts");
  await input.press("Enter");
  await expect(zone).toContainText("The selected code defines a value");
  const firstRequest = JSON.stringify(requests.at(-1));
  expect(firstRequest).toContain("unsaved note"); expect(firstRequest).toContain("reference-content");
  expect(firstRequest).toContain('const value = 1;');

  await send("change");
  await expect(page.locator(".inline-chat-added-lines")).toContainText("const value = 2;");
  await expect(page.locator(".inline-chat-removed").first()).toBeVisible();
  expect(readFileSync(mainPath, "utf8")).toBe(original);
  await send("follow-up");
  await expect(page.locator(".inline-chat-added-lines")).toContainText("const value = 3;");
  await expect(lines).toContainText("const value = 1;");
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.setViewportSize({ width: 800, height: 700 });
  await expect(zone.getByRole("button", { name: "Accept changes" })).toBeInViewport();
  await page.screenshot({ path: testInfo.outputPath("inline-review.png") });
  await zone.getByRole("button", { name: "Cancel and close" }).click();
  await expect(zone).toHaveCount(0); await expect(lines).toContainText("unsaved note");
  await openChat(); await send("change");
  await zone.getByRole("button", { name: "Accept changes" }).click();
  await expect(zone).toHaveCount(0); await expect(lines).toContainText("const value = 2;");
  expect(readFileSync(mainPath, "utf8")).toBe(original);
  await page.keyboard.press("Control+z");
  await expect(lines).toContainText("const value = 1;"); await expect(lines).toContainText("unsaved note");

  await openChat(); await send("change");
  await editor.focus(); await page.keyboard.press("Control+End"); await page.keyboard.insertText("// newer manual edit\n");
  await expect(zone.getByRole("button", { name: "Accept changes" })).toBeDisabled();
  await zone.getByRole("button", { name: "Regenerate from current file" }).click();
  await expect(zone.getByRole("button", { name: "Accept changes" })).toBeEnabled();
  await expect(lines).toContainText("newer manual edit");
  await zone.getByRole("button", { name: "Cancel and close" }).click();

  await openChat(); await input.fill("hold"); await input.press("Enter");
  await expect(zone.getByRole("button", { name: "Stop response" })).toBeVisible();
  expect(await zone.evaluate(element => getComputedStyle(element).animationName)).toBe("none");
  await openFile("helper.ts"); await expect(zone).toHaveCount(0);
  await openChat(); await send("explain");
  await openFile("main.ts"); await expect(zone.getByRole("button", { name: "Stop response" })).toBeVisible();
  await zone.getByRole("button", { name: "Stop response" }).click();
  await expect(zone).toContainText("Stopped");
  await expect.poll(() => held.size).toBe(0);
  await page.evaluate(() => { location.hash = "#/home"; });
  await expect(zone).toHaveCount(0);
  await page.evaluate(() => { location.hash = "#/code"; });
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await expect(zone).toContainText("Stopped");
  await page.keyboard.press("Control+w");
  await page.getByRole("button", { name: "Discard", exact: true }).click();
  await openFile("main.ts"); await expect(zone).toHaveCount(0);

  await page.keyboard.press("Control+n");
  await openChat(); await send("write empty");
  await zone.getByRole("button", { name: "Accept changes" }).click();
  await expect(lines).toContainText("Hello 😀");
  await openChat(); await send("emoji");
  await page.keyboard.press("Control+Shift+s");
  await page.getByLabel("File name", { exact: true }).fill("inline-saved.txt");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(zone).toContainText("file location has changed");
  const savedInlineText = readFileSync(join(workspace, "inline-saved.txt"), "utf8");
  expect(savedInlineText).toMatch(/^Hello 😀\r?\n$/);
  await expect(zone.getByRole("button", { name: "Accept changes" })).toBeDisabled();
  await zone.getByRole("button", { name: "Regenerate from current file" }).click();
  await expect(zone.getByRole("button", { name: "Accept changes" })).toBeEnabled();
  await zone.getByRole("button", { name: "Accept changes" }).click();
  await expect(lines).toContainText("Hello 😃");
  expect(readFileSync(join(workspace, "inline-saved.txt"), "utf8")).toBe(savedInlineText);
  await page.keyboard.press("Control+z");
  await expect(lines).toContainText("Hello 😀");
  await openChat(); await send("delete all");
  await zone.getByRole("button", { name: "Accept changes" }).click();
  await expect(lines).not.toContainText("Hello");

  // The command palette and editor context menu expose the same conversation.
  await openFile("main.ts");
  await page.keyboard.press("Control+Shift+p");
  await page.getByLabel("Command Palette").fill("Inline Chat");
  await page.getByRole("option", { name: /Inline Chat/ }).click();
  await expect(input).toBeFocused();
  await zone.getByRole("button", { name: "Cancel and close" }).click();
  await lines.click({ button: "right" });
  // Monaco ignores mouse-up for its first 100 ms to prevent accidental picks.
  await page.getByRole("menuitem", { name: /Inline Chat/ }).click({ delay: 150 });
  await expect(input).toBeFocused();

  // Side chat can finish independently while the inline request keeps running.
  await page.setViewportSize({ width: 1280, height: 900 });
  await page.emulateMedia({ reducedMotion: "no-preference" });
  await input.fill("hold"); await input.press("Enter");
  await expect(zone.getByRole("button", { name: "Stop response" })).toBeVisible();
  expect(await zone.evaluate(element => getComputedStyle(element).animationName)).toBe("inline-chat-breathe");
  await page.getByRole("button", { name: "Open code assistant" }).click();
  const sideInput = page.getByLabel("Message Echo about this code");
  await sideInput.fill("side response @helper");
  await expect(page.locator(".code-chat-surface").getByRole("option").first()).toBeVisible();
  await sideInput.press("Tab"); await sideInput.press("Enter");
  await expect(page.locator(".code-chat-surface .chat-final-content").last()).toContainText("Side chat response.");
  await expect(zone.getByRole("button", { name: "Stop response" })).toBeVisible();
  await zone.getByRole("button", { name: "Stop response" }).click();
  await expect.poll(() => held.size).toBe(0);
  await page.getByRole("button", { name: "Close chat", exact: true }).click();
  await zone.getByRole("button", { name: "Cancel and close" }).click();

  // Acceptance re-reads disk, even before a watcher reports a conflict.
  await openChat(); await send("change");
  await editor.focus(); await page.keyboard.press("Control+End"); await page.keyboard.insertText("// keep my edit\n");
  await zone.getByRole("button", { name: "Regenerate from current file" }).click();
  await expect(zone.getByRole("button", { name: "Accept changes" })).toBeEnabled();
  writeFileSync(mainPath, original + "// external edit\r\n");
  // A watcher may disable acceptance before the click; otherwise the fresh read does.
  await zone.getByRole("button", { name: "Accept changes" }).evaluate((button: HTMLButtonElement) => button.click());
  await expect(zone.getByRole("button", { name: "Accept changes" })).toBeDisabled();
  await expect(zone).toContainText("changed on disk");
  await expect(lines).toContainText("const value = 1;");
  await expect(lines).toContainText("keep my edit");
  expect(readFileSync(mainPath, "utf8")).toBe(original + "// external edit\r\n");
  await page.reload();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("aria-busy", "false");
  await expect(zone).toHaveCount(0);
  expect(errors).toEqual([]);
});
