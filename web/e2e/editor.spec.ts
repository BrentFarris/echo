import { expect, test, type Locator, type Page } from "@playwright/test";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { createServer, type Server as HTTPServer } from "node:http";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));
const password = "Echo-E2E-Password!";

test.describe.configure({ mode: "serial" });

let fakeLLM: HTTPServer;
let fakeLLMPort = 0;
let fakeStreamRound = 0;
let releaseScrollStreamChunk: (() => void) | null = null;

async function dragToTreeRow(page: Page, source: Locator, target: Locator): Promise<void> {
  await expect(source).toHaveAttribute("draggable", "true");
  await expect(source).toBeVisible();
  await expect(target).toBeVisible();
  const sourceBox = await source.boundingBox();
  const targetBox = await target.boundingBox();
  expect(sourceBox).not.toBeNull();
  expect(targetBox).not.toBeNull();
  await page.mouse.move(sourceBox!.x + sourceBox!.width / 2, sourceBox!.y + sourceBox!.height / 2);
  await page.mouse.down();
  try {
    await page.mouse.move(sourceBox!.x + sourceBox!.width / 2 + 8, sourceBox!.y + sourceBox!.height / 2, { steps: 2 });
    await page.mouse.move(targetBox!.x + targetBox!.width / 2, targetBox!.y + targetBox!.height / 2, { steps: 8 });
  } finally {
    await page.mouse.up();
  }
}

async function dragTabTo(page: Page, source: Locator, target: Locator, position: "before" | "after"): Promise<void> {
  await expect(source).toHaveAttribute("draggable", "true");
  await expect(source).toBeVisible();
  await expect(target).toBeVisible();
  const sourceBox = await source.boundingBox();
  const targetBox = await target.boundingBox();
  expect(sourceBox).not.toBeNull();
  expect(targetBox).not.toBeNull();
  await page.mouse.move(sourceBox!.x + sourceBox!.width / 2, sourceBox!.y + sourceBox!.height / 2);
  await page.mouse.down();
  try {
    await page.mouse.move(sourceBox!.x + sourceBox!.width / 2 + 8, sourceBox!.y + sourceBox!.height / 2, { steps: 2 });
    const targetX = targetBox!.x + targetBox!.width * (position === "before" ? 0.25 : 0.75);
    await page.mouse.move(targetX, targetBox!.y + targetBox!.height / 2, { steps: 8 });
    await expect(page.locator(".code-tab.is-dragging")).toHaveCount(1);
    await expect(page.locator(`.code-tab.is-drop-${position}`)).toHaveCount(1);
  } finally {
    await page.mouse.up();
  }
}

test.beforeAll(async () => {
  fakeLLM = createServer(async (request, response) => {
    let raw = "";
    for await (const chunk of request) raw += chunk.toString();
    const body = JSON.parse(raw || "{}");
    if (body.stream !== true) {
      response.writeHead(200, { "Content-Type": "application/json" });
      response.end(JSON.stringify({
        choices: [{ message: { role: "assistant", content: "## Goal\nContinue the E2E task.\n## Constraints & Preferences\nNone.\n## Progress\n### Done\nEarlier work.\n### In Progress\nContinue.\n### Blocked\nNone.\n## Key Decisions\nNone.\n## Relevant Files & Artifacts\nworkspace/main.go\n## Commands, Tests & Errors\nNone.\n## Next Steps\nContinue.\n## Critical Exact Context\nE2E." } }],
        usage: { prompt_tokens: 100, completion_tokens: 50, total_tokens: 150 },
      }));
      return;
    }
    fakeStreamRound++;
    response.writeHead(200, { "Content-Type": "text/event-stream", "Cache-Control": "no-cache" });
    if (JSON.stringify(body.messages || []).includes("scroll-follow-e2e")) {
      const writeContent = (content: string) => response.write(`data: ${JSON.stringify({ choices: [{ delta: { content } }] })}\n\n`);
      writeContent(Array.from({ length: 100 }, (_, index) => `Scroll follow first ${index + 1}`).join("\n\n"));
      await new Promise<void>((resolveChunk) => { releaseScrollStreamChunk = resolveChunk; });
      releaseScrollStreamChunk = null;
      writeContent(Array.from({ length: 30 }, (_, index) => `Scroll follow manual ${index + 1}`).join("\n\n"));
      await new Promise<void>((resolveChunk) => { releaseScrollStreamChunk = resolveChunk; });
      releaseScrollStreamChunk = null;
      writeContent("Scroll follow resumed at the tail.");
      response.write(`data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: "stop" }] })}\n\n`);
      response.write(`data: ${JSON.stringify({ choices: [], usage: { prompt_tokens: 200, completion_tokens: 130, total_tokens: 330 } })}\n\n`);
      response.end("data: [DONE]\n\n");
      return;
    }
    if (fakeStreamRound === 1) {
      await new Promise((resolveWait) => setTimeout(resolveWait, 900));
      response.write(`data: ${JSON.stringify({ choices: [{ delta: { tool_calls: [{ index: 0, id: "call-e2e-read", type: "function", function: { name: "filesystem_read_text", arguments: "{\"path\":\"workspace/main.go\"}" } }] } }] })}\n\n`);
      response.write(`data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: "tool_calls" }] })}\n\n`);
    } else {
      response.write(`data: ${JSON.stringify({ choices: [{ delta: { content: "Finished after queued compression." } }] })}\n\n`);
      response.write(`data: ${JSON.stringify({ choices: [{ delta: {}, finish_reason: "stop" }] })}\n\n`);
    }
    response.write(`data: ${JSON.stringify({ choices: [], usage: { prompt_tokens: 200, completion_tokens: 20, total_tokens: 220 } })}\n\n`);
    response.end("data: [DONE]\n\n");
  });
  await new Promise<void>((resolveListen) => fakeLLM.listen(0, "127.0.0.1", resolveListen));
  const address = fakeLLM.address();
  if (!address || typeof address === "string") throw new Error("Fake LLM did not bind a TCP port");
  fakeLLMPort = address.port;
});

test.afterAll(async () => {
  releaseScrollStreamChunk?.();
  releaseScrollStreamChunk = null;
  await new Promise<void>((resolveClose, rejectClose) => fakeLLM.close((error) => error ? rejectClose(error) : resolveClose()));
});

test("first-run auth and the real Monaco filesystem workflow", async ({ page }) => {
  // This is a deliberately broad, real-process acceptance scenario. Plugin
  // installation adds an additional reviewed lifecycle and browser-isolation
  // pass before the existing editor/terminal/mobile coverage.
  test.setTimeout(420_000);
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8")) as {
    setupCode: string;
    workspace: string;
    secondaryWorkspace: string;
  };
  const mainPath = join(state.workspace, "main.go");
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

  // Exercise the real browser/WebSocket lifecycle: queue manual compression
  // while a provider response is active, then verify the pending compression
  // checkpoint does not interrupt the tool result or the following model round.
  await page.evaluate(async ({ port }) => {
    const response = await fetch("/api/settings");
    const payload = await response.json();
    const settings = payload.data.settings;
    const endpoint = {
      ...settings.endpoints[0], endpoint: `http://127.0.0.1:${port}/v1`, model: "echo-e2e-context",
      contextLength: 65536, maxTokens: 512, contextCompressionEnabled: false,
      contextCompressionThresholdPercent: 70,
    };
    settings.endpoints = [endpoint];
    settings.endpointSelection = {
      chat: endpoint.id, research: endpoint.id, vision: endpoint.id,
      kanbanDecompose: endpoint.id, kanban: endpoint.id, inlineCode: endpoint.id,
    };
    const saved = await fetch("/api/settings", {
      method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ settings }),
    });
    if (!saved.ok) throw new Error(`Could not configure fake LLM: HTTP ${saved.status}`);
  }, { port: fakeLLMPort });
  await page.reload();
  await expect(page.locator(".app-shell")).toBeVisible();
  await expect(page.locator("[data-mode-label]")).toHaveText("General");
  await page.evaluate(({ workspaceId }) => new Promise<void>((resolveSend, rejectSend) => {
    const protocol = location.protocol === "https:" ? "wss" : "ws";
    const socket = new WebSocket(`${protocol}://${location.host}/ws`);
    const timer = window.setTimeout(() => {
      socket.close();
      rejectSend(new Error("Timed out sending the compression E2E chat turn"));
    }, 10_000);
    socket.addEventListener("message", (event) => {
      const message = JSON.parse(String(event.data));
      if (message.type === "welcome") {
        socket.send(JSON.stringify({ type: "session_subscribe", workspaceId }));
      } else if (message.type === "session_snapshot") {
        socket.send(JSON.stringify({
          type: "chat_send", workspaceId, chatId: message.activeChatId,
          requestId: "e2e-context-compression", agentModeId: "general",
          message: "Read main.go, then continue after context compression.",
        }));
      } else if (message.type === "session_event" && message.event?.type === "turn_started") {
        window.clearTimeout(timer);
        socket.close();
        resolveSend();
      }
    });
    socket.addEventListener("error", () => {
      window.clearTimeout(timer);
      rejectSend(new Error("Compression E2E WebSocket failed"));
    });
  }), { workspaceId: workspaceIDs.primary });
  await expect(page.locator(".chat-message-user")).toContainText("Read main.go");
  await page.locator("[data-chat-more-trigger]").click();
  await page.getByRole("menuitem", { name: "Compress context now" }).click();
  await expect(page.locator(".chat-compression-item")).toContainText("Context compression queued");
  await expect(page.locator(".chat-tool-item", { hasText: "filesystem_read_text" })).toHaveCount(1);
  await expect(page.locator(".chat-final-content")).toContainText("Finished after queued compression.");
  await expect(page.locator(".chat-compression-item")).toContainText("Context compression skipped");

  // Optional plugins install through a real reviewed stage. The Calculator
  // stays isolated and alive while Echo's statically owned routes change.
  await page.evaluate(async () => {
    const request = async (path: string, body: unknown) => {
      const response = await fetch(path, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      const payload = await response.json();
      if (!response.ok || payload.ok === false) throw new Error(payload.error || `HTTP ${response.status}`);
      return payload.data;
    };
    const staged = await request("/api/plugins/stages", { source: { type: "builtin", builtin: "calculator" } });
    await request(`/api/plugins/stages/${encodeURIComponent(staged.stage.id)}/approve`, { scope: "global", enable: true });
  });
  const calculatorButton = page.getByRole("button", { name: "Calculator", exact: true });
  await expect(calculatorButton).toBeVisible();
  await calculatorButton.click();
  const calculatorWindow = page.locator(".plugin-floating-window", { has: page.locator('iframe[title="Calculator plugin"]') });
  await expect(calculatorWindow).toBeVisible();
  const calculator = page.frameLocator('iframe[title="Calculator plugin"]');
  await calculator.getByRole("button", { name: "7", exact: true }).click();
  await calculator.getByRole("button", { name: "×", exact: true }).click();
  await calculator.getByRole("button", { name: "6", exact: true }).click();
  await calculator.getByRole("button", { name: "=", exact: true }).click();
  await expect(calculator.locator("#display")).toHaveText("42");
  const isolation = await calculator.locator("body").evaluate(async () => {
    let dom = "available";
    let api = "available";
    try { void window.parent.document.body; } catch { dom = "blocked"; }
    try { await fetch("/api/plugins"); } catch { api = "blocked"; }
    return { dom, api };
  });
  expect(isolation).toEqual({ dom: "blocked", api: "blocked" });
  const beforeMove = await calculatorWindow.boundingBox();
  const header = calculatorWindow.locator(".plugin-window-header");
  const headerBox = await header.boundingBox();
  if (beforeMove && headerBox) {
    await page.mouse.move(headerBox.x + 40, headerBox.y + 18);
    await page.mouse.down();
    await page.mouse.move(headerBox.x - 20, headerBox.y + 55, { steps: 4 });
    await page.mouse.up();
    await expect.poll(async () => (await calculatorWindow.boundingBox())?.x).not.toBe(beforeMove.x);
  }
  await page.getByRole("button", { name: "Explorer", exact: true }).click();
  await expect(page).toHaveURL(/#\/code$/);
  await expect(calculatorWindow).toBeVisible();
  await page.getByRole("button", { name: "Source Control", exact: true }).click();
  await expect(page).toHaveURL(/#\/code\?sidebar=git$/);
  await expect(calculatorWindow).toBeVisible();
  await page.getByRole("button", { name: "Chat", exact: true }).click();
  await expect(page).toHaveURL(/#\/home$/);
  await expect(calculatorWindow).toBeVisible();
  await page.evaluate(async () => {
    const response = await fetch("/api/plugins/calculator/actions", {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action: "disable-global" }),
    });
    if (!response.ok) throw new Error(`HTTP ${response.status}`);
  });
  await expect(calculatorWindow).toHaveCount(0);
  await expect(calculatorButton).toHaveCount(0);

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
  await page.getByRole("button", { name: "Chat", exact: true }).click();
  await expect(page.locator(".app-shell")).toBeVisible();

  // The terminal is a single workspace session shared across Chat and Code.
  await page.getByRole("button", { name: "Open terminal" }).click();
  await expect(page.locator(".terminal-dock")).toHaveClass(/is-open/);
  await expect(page.locator(".terminal-status-text")).toHaveText("Running");
  await page.locator(".terminal-xterm-instance .xterm-helper-textarea").focus();
  await page.keyboard.type("echo ECHO_TERMINAL_ROUTE_OK");
  await page.keyboard.press("Enter");
  await expect(page.locator(".terminal-xterm-instance .xterm-rows")).toContainText("ECHO_TERMINAL_ROUTE_OK");

  await page.getByRole("button", { name: "Source Control", exact: true }).click();
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

  // Explorer drag/drop moves files into folders while preserving the open tab.
  const renamedBeforeMove = page.locator(".code-tree-row", { hasText: "renamed.py" });
  const workspaceRoot = page.locator('.code-tree-row[data-tree-root="true"]').first();
  const explorerTree = page.locator("[data-code-tree]:visible").first();
  await explorerTree.evaluate((element) => { element.scrollTop = 0; });
  await expect(workspaceRoot).toBeVisible();
  await expect(renamedBeforeMove).toBeVisible();
  await dragToTreeRow(page, renamedBeforeMove, workspaceRoot);
  await expect.poll(() => existsSync(join(state.workspace, "renamed.py"))).toBe(true);
  await expect.poll(() => existsSync(join(state.workspace, "nested", "renamed.py"))).toBe(false);
  await expect(page.getByRole("tab", { name: /renamed\.py/ })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".view-lines")).toContainText("print('echo')");
  await expect(page.locator(".code-tree-row", { hasText: "renamed.py" })).toHaveAttribute("aria-selected", "true");

  await page.keyboard.press("Control+p");
  await page.getByLabel("Go to File").fill("main.go");
  const mainGoResult = page.getByRole("option", { name: /main\.go/ });
  await expect(mainGoResult).toBeVisible();
  await mainGoResult.click();
  await expect(page.getByRole("tab", { name: /main\.go/ })).toBeVisible();
  await expect(page.locator(".code-tree-row", { hasText: "main.go" })).toHaveAttribute("aria-selected", "true");

  // Clicking already-open files updates the explorer selection immediately.
  const mainTreeRow = page.locator(".code-tree-row", { hasText: "main.go" });
  const renamedTreeRow = page.locator(".code-tree-row", { hasText: "renamed.py" });
  await renamedTreeRow.click();
  await expect(renamedTreeRow).toHaveAttribute("aria-selected", "true");
  await expect(renamedTreeRow).toHaveClass(/is-selected/);
  await expect(mainTreeRow).toHaveAttribute("aria-selected", "false");
  await expect(page.locator(".view-lines")).toContainText("print('echo')");
  await mainTreeRow.click();
  await expect(mainTreeRow).toHaveAttribute("aria-selected", "true");
  await expect(mainTreeRow).toHaveClass(/is-selected/);
  await expect(renamedTreeRow).toHaveAttribute("aria-selected", "false");
  await expect(page.locator(".view-lines")).toContainText("package main");

  // Ctrl+F searches within the active file using Monaco's find widget.
  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+f");
  const editorFind = page.getByRole("textbox", { name: "Find", exact: true });
  await expect(editorFind).toBeFocused();
  await editorFind.fill("func main");
  await expect(page.locator(".find-widget:visible .matchesCount")).toHaveText("1 of 1");
  await page.keyboard.press("Escape");
  await expect(editorFind).not.toBeVisible();

  // Switching existing tabs updates both Monaco and the tab strip's active state.
  const mainTab = page.getByRole("tab", { name: /main\.go/ });
  const renamedTab = page.getByRole("tab", { name: /renamed\.py/ });
  const ensureMainTab = async () => {
    try {
      await mainTab.waitFor({ state: "visible", timeout: 2_000 });
      return;
    } catch {
      // Route transitions and reloads restore persisted tabs asynchronously.
      // If main.go was only a preview and was not persisted, reopen it through
      // the visible Explorer instead of racing the global keyboard handler.
    }

    const tree = page.locator("[data-code-tree]:visible").first();
    await expect(tree).toBeVisible();
    const root = tree.locator('.code-tree-row[data-tree-root="true"]').first();
    await expect(root).toBeVisible();
    if (await root.getAttribute("aria-expanded") !== "true") await root.click();
    await tree.evaluate((element) => { element.scrollTop = 0; });
    const mainFile = tree.locator(".code-tree-label", { hasText: "main.go" });
    await expect(mainFile).toBeVisible();
    await mainFile.dblclick();
    await expect(mainTab).toBeVisible();
  };
  await renamedTab.click();
  await expect(renamedTab).toHaveAttribute("aria-selected", "true");
  await expect(renamedTab).toHaveClass(/is-active/);
  await expect(mainTab).toHaveAttribute("aria-selected", "false");
  await expect(page.locator(".view-lines")).toContainText("print('echo')");
  await mainTab.click();
  await expect(mainTab).toHaveAttribute("aria-selected", "true");
  await expect(mainTab).toHaveClass(/is-active/);
  await expect(renamedTab).toHaveAttribute("aria-selected", "false");
  await expect(page.locator(".view-lines")).toContainText("package main");

  // Ctrl+Tab keeps the existing immediate MRU activation behavior while a
  // picker-style switcher visualizes the stable cycle until Ctrl is released.
  const definitionTreeRow = page.locator(".code-tree-label", { hasText: "definition.go" });
  await definitionTreeRow.click();
  const definitionTab = page.getByRole("tab", { name: /definition\.go/ });
  await expect(definitionTab).toHaveAttribute("aria-selected", "true");
  await definitionTab.dblclick();
  await mainTab.click();
  const editorInput = page.getByRole("textbox", { name: "Editor content" });
  const mruSwitcher = page.getByRole("listbox", { name: "Recently used editors" });

  await page.keyboard.down("Control");
  await page.keyboard.press("Tab");
  await expect(mruSwitcher).toBeVisible();
  await expect(mruSwitcher.getByRole("option")).toHaveCount(3);
  const sourceOption = mruSwitcher.getByRole("option", { name: /main\.go/ });
  const definitionOption = mruSwitcher.getByRole("option", { name: /definition\.go/ });
  const renamedOption = mruSwitcher.getByRole("option", { name: /renamed\.py/ });
  await expect(mruSwitcher.getByRole("option").nth(0)).toHaveAttribute("data-mru-tab-id", await mainTab.getAttribute("data-tab-id") || "");
  await expect(sourceOption).toHaveAttribute("aria-selected", "false");
  await expect(definitionOption).toHaveAttribute("aria-selected", "true");
  await expect(definitionOption.locator(".code-mru-switcher-context")).toContainText("workspace");
  await expect(definitionTab).toHaveAttribute("aria-selected", "true");
  await expect(editorInput).toBeFocused();

  await page.keyboard.press("Tab");
  await expect(renamedOption).toHaveAttribute("aria-selected", "true");
  await expect(renamedTab).toHaveAttribute("aria-selected", "true");
  await expect(editorInput).toBeFocused();
  await page.keyboard.press("Tab");
  await expect(sourceOption).toHaveAttribute("aria-selected", "true");
  await expect(mainTab).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Tab");
  await page.keyboard.down("Shift");
  await page.keyboard.press("Tab");
  await page.keyboard.up("Shift");
  await expect(definitionOption).toHaveAttribute("aria-selected", "true");
  await expect(definitionTab).toHaveAttribute("aria-selected", "true");
  await page.keyboard.up("Control");
  await expect(mruSwitcher).toHaveCount(0);
  await expect(definitionTab).toHaveAttribute("aria-selected", "true");

  // Releasing Ctrl commits the chosen tab to the front of the next MRU cycle.
  await page.keyboard.down("Control");
  await page.keyboard.press("Tab");
  await expect(mruSwitcher.getByRole("option", { name: /main\.go/ })).toHaveAttribute("aria-selected", "true");
  await expect(mainTab).toHaveAttribute("aria-selected", "true");
  await page.keyboard.up("Control");

  // Clicking a different row commits it immediately and returns focus to its
  // editor even though the modifier remains held until after the click.
  await page.keyboard.down("Control");
  await page.keyboard.press("Tab");
  await mruSwitcher.getByRole("option", { name: /renamed\.py/ }).click();
  await expect(mruSwitcher).toHaveCount(0);
  await expect(renamedTab).toHaveAttribute("aria-selected", "true");
  await expect(editorInput).toBeFocused();
  await page.keyboard.up("Control");

  // Backdrop clicks and window blur accept the current selection and clean up
  // the switcher without restoring the tab that started the cycle.
  await mainTab.click();
  await page.keyboard.down("Control");
  await page.keyboard.press("Tab");
  const activeAfterBackdrop = await page.locator(".code-tab.is-active").textContent();
  await page.locator(".code-mru-switcher-overlay").click({ position: { x: 4, y: 4 } });
  await expect(mruSwitcher).toHaveCount(0);
  await expect(page.locator(".code-tab.is-active")).toHaveText(activeAfterBackdrop!);
  await expect(editorInput).toBeFocused();
  await page.keyboard.up("Control");
  await page.keyboard.down("Control");
  await page.keyboard.press("Tab");
  await expect(mruSwitcher).toBeVisible();
  await page.evaluate(() => window.dispatchEvent(new Event("blur")));
  await expect(mruSwitcher).toHaveCount(0);
  await page.keyboard.up("Control");

  // Closing the selected tab prunes it from the visible stable cycle. The next
  // Tab press selects the remaining candidate and normal cycling continues.
  await mainTab.click();
  await page.keyboard.down("Control");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Tab");
  await expect(definitionTab).toHaveAttribute("aria-selected", "true");
  await page.keyboard.press("w");
  await expect(definitionTab).toHaveCount(0);
  await expect(mruSwitcher.getByRole("option", { name: /definition\.go/ })).toHaveCount(0);
  await expect(mruSwitcher.getByRole("option")).toHaveCount(2);
  await expect(mruSwitcher.getByRole("option", { name: /main\.go/ })).toBeVisible();
  await page.keyboard.press("Tab");
  await expect(renamedTab).toHaveAttribute("aria-selected", "true");
  await page.keyboard.up("Control");

  // Leaving Code while a cycle is open disposes its body-level overlay. Return
  // to the editor and restore main.go for the remaining editor scenarios.
  await mainTab.click();
  await page.keyboard.down("Control");
  await page.keyboard.press("Tab");
  await expect(mruSwitcher).toBeVisible();
  await page.evaluate(() => { window.location.hash = "#/home"; });
  await expect(page).toHaveURL(/#\/home$/);
  await expect(mruSwitcher).toHaveCount(0);
  await page.keyboard.up("Control");
  await page.getByRole("button", { name: "Explorer", exact: true }).click();
  await expect(page).toHaveURL(/#\/code$/);
  await ensureMainTab();
  await mainTab.click();
  await expect(page.locator(".view-lines")).toContainText("package main");

  // Command-palette selections form a persistent five-item MRU. Section
  // headings remain outside the option index, so Enter immediately repeats
  // the last command and search continues to rank matching recents first.
  const commandHistoryKey = "echo.code.commandPalette.recent.v1";
  await page.evaluate((key) => localStorage.removeItem(key), commandHistoryKey);
  await page.reload();
  await expect(page.locator(".code-app-shell")).toBeVisible();
  const explorerRoot = page.locator('.code-tree-row[data-tree-root="true"]').first();
  if (await explorerRoot.getAttribute("aria-expanded") !== "true") await explorerRoot.click();
  await expect(explorerRoot).toHaveAttribute("aria-expanded", "true");

  await page.keyboard.press("Control+Shift+P");
  await page.getByLabel("Command Palette").fill("Explorer: Collapse All");
  await page.keyboard.press("Enter");
  await expect(explorerRoot).toHaveAttribute("aria-expanded", "false");

  await explorerRoot.click();
  await expect(explorerRoot).toHaveAttribute("aria-expanded", "true");
  await page.keyboard.press("Control+Shift+P");
  let paletteOptions = page.locator(".code-picker-list").getByRole("option");
  await expect(page.locator(".code-picker-section-label", { hasText: "Recent" })).toBeVisible();
  await expect(paletteOptions.first()).toContainText("Explorer: Collapse All");
  await page.keyboard.press("Enter");
  await expect(explorerRoot).toHaveAttribute("aria-expanded", "false");

  await page.keyboard.press("Control+Shift+P");
  await page.getByLabel("Command Palette").fill("Explorer: Refresh");
  await page.keyboard.press("Enter");
  await page.keyboard.press("Control+Shift+P");
  paletteOptions = page.locator(".code-picker-list").getByRole("option");
  await expect(paletteOptions.nth(0)).toContainText("Explorer: Refresh");
  await expect(paletteOptions.nth(1)).toContainText("Explorer: Collapse All");
  await expect(page.getByRole("option", { name: /Explorer: Refresh/ })).toHaveCount(1);
  await expect(page.getByRole("option", { name: /Explorer: Collapse All/ })).toHaveCount(1);
  await page.getByLabel("Command Palette").fill("Explorer: Collapse All");
  await page.keyboard.press("Enter");

  await page.keyboard.press("Control+Shift+P");
  paletteOptions = page.locator(".code-picker-list").getByRole("option");
  await expect(paletteOptions.nth(0)).toContainText("Explorer: Collapse All");
  await expect(paletteOptions.nth(1)).toContainText("Explorer: Refresh");
  await page.getByLabel("Command Palette").fill("Explorer");
  await expect(page.locator(".code-picker-section-label")).toHaveCount(0);
  await expect(paletteOptions.nth(0)).toContainText("Explorer: Collapse All");
  await expect(paletteOptions.nth(1)).toContainText("Explorer: Refresh");
  await page.keyboard.press("Escape");

  await page.reload();
  await expect(page.locator(".code-app-shell")).toBeVisible();
  await page.keyboard.press("Control+Shift+P");
  paletteOptions = page.locator(".code-picker-list").getByRole("option");
  await expect(paletteOptions.nth(0)).toContainText("Explorer: Collapse All");
  await expect(paletteOptions.nth(1)).toContainText("Explorer: Refresh");
  await page.keyboard.press("Escape");

  // Echo exposes Monaco's native VS Code-style case transforms through its
  // command palette, preserving selection and grouping each edit for Undo.
  await page.keyboard.press("Control+n");
  const scratchLines = page.locator("[data-monaco-host] .view-line");
  const chooseCaseTransform = async (label: string) => {
    await page.keyboard.press("Control+Shift+P");
    const palette = page.getByLabel("Command Palette");
    await palette.fill(label);
    await expect(page.getByRole("option", { name: new RegExp(label) })).toBeVisible();
    await page.keyboard.press("Enter");
  };
  const undoCaseTransform = async () => {
    await page.keyboard.press("Control+Shift+P");
    await page.getByLabel("Command Palette").fill("Editor: Undo");
    await page.keyboard.press("Enter");
  };
  const replaceScratchText = async (text: string) => {
    await editorInput.focus();
    await page.keyboard.press("Control+a");
    await page.keyboard.insertText(text);
  };
  for (const transform of [
    { label: "Transform to Uppercase", input: "MiXeD Case", output: "MIXED CASE" },
    { label: "Transform to Lowercase", input: "MiXeD Case", output: "mixed case" },
    { label: "Transform to Camel Case", input: "hello world-test_value", output: "helloWorldTestValue" },
    { label: "Transform to Kebab Case", input: "helloWorld_testValue", output: "hello-world-test-value" },
    { label: "Transform to Pascal Case", input: "hello world-test_value", output: "HelloWorldTestValue" },
    { label: "Transform to Snake Case", input: "helloWorldValue", output: "hello_world_value" },
    { label: "Transform to Title Case", input: "hELLO wORLD", output: "Hello World" },
  ]) {
    await replaceScratchText(transform.input);
    await page.keyboard.press("Control+a");
    await chooseCaseTransform(transform.label);
    await expect(scratchLines).toHaveText(transform.output);
    await undoCaseTransform();
    await expect(scratchLines).toHaveText(transform.input);
  }

  // Multiple selections transform together in one undo step.
  await replaceScratchText("alpha\nbeta");
  await page.keyboard.press("Control+Home");
  await page.keyboard.press("Shift+End");
  await page.keyboard.press("Control+Alt+ArrowDown");
  await chooseCaseTransform("Transform to Uppercase");
  await expect(scratchLines).toHaveCount(2);
  await expect(scratchLines.nth(0)).toHaveText("ALPHA");
  await expect(scratchLines.nth(1)).toHaveText("BETA");
  await undoCaseTransform();
  await expect(scratchLines.nth(0)).toHaveText("alpha");
  await expect(scratchLines.nth(1)).toHaveText("beta");

  // A plain caret uses Monaco's native word-under-caret fallback.
  await replaceScratchText("before mixedWord after");
  await page.keyboard.press("Control+Home");
  for (let offset = 0; offset < 9; offset++) await page.keyboard.press("ArrowRight");
  await chooseCaseTransform("Transform to Uppercase");
  await expect(scratchLines).toHaveText("before MIXEDWORD after");
  await undoCaseTransform();
  await expect(scratchLines).toHaveText("before mixedWord after");

  await page.keyboard.press("Control+w");
  await page.getByRole("button", { name: "Discard", exact: true }).click();
  await ensureMainTab();
  await mainTab.click();
  await expect(page.locator(".view-lines")).toContainText("package main");

  // Selection and caret occurrence highlighting use Monaco's native subtle
  // decorations, including markers in the overview ruler and minimap.
  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+Home");
  await page.keyboard.press("End");
  await page.keyboard.press("Control+Shift+ArrowLeft");
  await expect(page.locator(".monaco-editor .selected-text")).toHaveCount(1);
  await expect(page.locator(".monaco-editor .selectionHighlight")).toHaveCount(1);
  await expect(page.locator(".monaco-editor canvas.decorationsOverviewRuler")).toBeVisible();
  await expect(page.locator(".monaco-editor .minimap")).toBeVisible();

  // Collapsing the selection keeps whole-word caret occurrences highlighted.
  await page.keyboard.press("ArrowLeft");
  await expect(page.locator(".monaco-editor .selected-text")).toHaveCount(0);
  await expect(page.locator(".monaco-editor .selectionHighlight")).toHaveCount(0);
  await expect(page.locator(".monaco-editor .wordHighlightText")).toHaveCount(2);

  // Leaving the word for an empty line clears all occurrence decorations.
  await page.keyboard.press("Control+Home");
  await page.keyboard.press("ArrowDown");
  await expect(page.locator(".monaco-editor .wordHighlightText")).toHaveCount(0);

  // Re-selecting the active tab keeps Monaco's live caret and selection. The
  // tab's stored view state can lag behind normal editing and must only be
  // restored after switching between different tabs.
  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+Home");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.down("Shift");
  await page.keyboard.press("End");
  await page.keyboard.up("Shift");
  const cursorBeforeReselect = await page.locator('[data-status="cursor"]').textContent();
  expect(cursorBeforeReselect).not.toBe("Ln 3, Col 1");
  await page.getByRole("tab", { name: /main\.go/ }).click();
  await expect(page.locator('[data-status="cursor"]')).toHaveText(cursorBeforeReselect!);
  await page.keyboard.press("ArrowLeft");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 3, Col 1");

  // A plain click inside selected text collapses the selection at that point.
  // Monaco's pointer handler delegates this case to its drag-and-drop
  // contribution even when the pointer never moves.
  await page.keyboard.down("Shift");
  await page.keyboard.press("End");
  await page.keyboard.up("Shift");
  const selectedText = page.locator(".monaco-editor .selected-text").first();
  await expect(selectedText).toBeVisible();
  const selectionBox = await selectedText.boundingBox();
  expect(selectionBox).not.toBeNull();
  await page.mouse.click(selectionBox!.x + selectionBox!.width / 2, selectionBox!.y + selectionBox!.height / 2);
  await expect(page.locator(".monaco-editor .selected-text")).toHaveCount(0);
  await expect(page.locator('[data-status="cursor"]')).not.toHaveText("Ln 3, Col 1");

  // Tab and Shift+Tab indent and outdent every line in a multi-line selection.
  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+Home");
  await page.keyboard.down("Shift");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.up("Shift");
  await page.keyboard.press("Tab");
  await page.keyboard.press("Control+s");
  await expect.poll(() => readFileSync(mainPath, "utf8")).toMatch(/^[\t ]+package main/);
  await page.keyboard.press("Control+Home");
  await page.keyboard.down("Shift");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.press("ArrowDown");
  await page.keyboard.up("Shift");
  await page.keyboard.press("Shift+Tab");
  await page.keyboard.press("Control+s");
  await expect.poll(() => readFileSync(mainPath, "utf8")).toMatch(/^package main\n\nfunc main\(\)/);

  // Workspace Search includes unsaved Monaco buffers, filters by glob, opens
  // the exact result range, previews replacement, and safely saves dirty text.
  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+End");
  await page.keyboard.press("Enter");
  await page.keyboard.type("// searchUnsavedToken");
  await page.keyboard.press("Control+Shift+F");
  const workspaceSearch = page.getByLabel("Search workspace");
  await expect(workspaceSearch).toBeFocused();
  await workspaceSearch.fill("searchUnsavedToken");
  await page.getByRole("button", { name: "Toggle Search Details" }).click();
  await page.getByLabel("Files to include").fill("*.go");
  const unsavedResult = page.getByRole("treeitem", { name: /searchUnsavedToken/ });
  await expect(unsavedResult).toBeVisible();
  await unsavedResult.click();
  await expect(page.locator(".view-lines")).toContainText("searchUnsavedToken");
  await page.keyboard.press("Control+Shift+H");
  const workspaceReplace = page.getByLabel("Replace in workspace");
  await expect(workspaceReplace).toBeVisible();
  await workspaceReplace.fill("searchSavedToken");
  await expect(page.locator(".code-search-preview small")).toContainText("searchSavedToken");
  await expect(unsavedResult).toHaveCSS("height", "22px");
  await expect(unsavedResult).toHaveCSS("padding-left", "40px");
  await page.getByRole("button", { name: /Replace result on line/ }).click();
  await page.getByRole("button", { name: "Replace", exact: true }).click();
  await expect.poll(() => readFileSync(mainPath, "utf8")).toContain("searchSavedToken");
  await expect(page.locator(".code-tab-dirty.is-visible")).toHaveCount(0);
  await page.getByRole("button", { name: "Explorer", exact: true }).click();

  await page.getByRole("tab", { name: /renamed\.py/ }).click({ button: "middle" });
  await expect(page.getByRole("tab", { name: /renamed\.py/ })).toHaveCount(0);
  await expect(page.getByRole("tab", { name: /main\.go/ })).toBeVisible();

  // Code Chat is collapsed by default, opens beneath the editor tabs, keeps
  // the compact reference picker, surfaces selected editor context, and
  // exposes an accessible width control.
  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+Home");
  await page.keyboard.down("Shift");
  await page.keyboard.press("End");
  await page.keyboard.up("Shift");
  const codeChatToggle = page.getByRole("button", { name: "Open code assistant" });
  const codeChatToggleControl = page.locator("[data-code-chat-toggle]");
  await expect(codeChatToggleControl).toHaveAttribute("aria-expanded", "false");
  await expect(page.locator("[data-code-chat-dock]")).toBeHidden();
  await codeChatToggle.click();
  await expect(page.locator("[data-code-chat-dock]")).toBeVisible();
  await expect(codeChatToggleControl).toHaveAttribute("aria-expanded", "true");

  // ESC while a file search/replace is active must dismiss the search first,
  // leaving the code chat open; a second ESC closes the chat.
  await page.keyboard.press("Control+Shift+F");
  const chatSearchInput = page.getByLabel("Search workspace");
  await expect(chatSearchInput).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.locator("[data-code-chat-dock]")).toBeVisible();
  await expect(codeChatToggleControl).toHaveAttribute("aria-expanded", "true");
  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+f");
  const chatFindInput = page.getByRole("textbox", { name: "Find", exact: true });
  await expect(chatFindInput).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(page.locator("[data-code-chat-dock]")).toBeVisible();
  await expect(codeChatToggleControl).toHaveAttribute("aria-expanded", "true");
  await page.keyboard.press("Escape");
  await expect(page.locator("[data-code-chat-dock]")).toBeHidden();
  await codeChatToggle.click();
  await expect(page.locator("[data-code-chat-dock]")).toBeVisible();
  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+Home");
  await page.keyboard.down("Shift");
  await page.keyboard.press("End");
  await page.keyboard.up("Shift");

  const selectedContextNotice = page.locator("[data-chat-context-notice]");
  await expect(selectedContextNotice).toHaveText("Selected context: main.go, line 1 will be included.");
  await page.locator(".view-lines").click();
  await page.keyboard.press("ArrowRight");
  await expect(selectedContextNotice).toBeHidden();
  const codeChatInput = page.getByLabel("Message Echo about this code");
  await codeChatInput.fill("Review @main");
  await expect(page.getByRole("option", { name: /main\.go/ })).toBeVisible();
  await page.getByRole("option", { name: /main\.go/ }).click();
  await expect(codeChatInput.locator("[data-chat-file-mention]")).toHaveAttribute("data-reference-path", /main\.go$/);
  const codeChatResizer = page.getByRole("separator", { name: "Resize Code Chat" });
  const widthBefore = Number(await codeChatResizer.getAttribute("aria-valuenow"));
  await codeChatResizer.focus();
  await page.keyboard.press("ArrowLeft");
  const widthAfter = Number(await codeChatResizer.getAttribute("aria-valuenow"));
  expect(widthAfter).toBeGreaterThan(widthBefore);

  // Persist the exact tabs, selection range, and explicit mention used by a
  // Code Chat prompt, then restore and navigate that context after reload.
  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+Home");
  await page.keyboard.down("Shift");
  await page.keyboard.press("End");
  await page.keyboard.up("Shift");
  await codeChatInput.focus();
  await page.keyboard.press("Enter");
  const promptResources = page.locator(".chat-message-user .chat-prompt-resources").last();
  await expect(promptResources).toBeVisible();
  await expect(promptResources.locator("summary")).toContainText("tab");
  await expect(promptResources.locator("summary")).toContainText("1 selection");
  await expect(promptResources.locator("summary")).toContainText("1 mention");
  await expect(page.locator(".chat-message-assistant.is-streaming")).toHaveCount(0);

  await codeChatInput.fill("Inspect @nest");
  const folderMention = page.locator("[data-chat-mention-option]", { has: page.locator(".chat-mention-kind", { hasText: "Folder" }) });
  await expect(folderMention).toBeVisible();
  await folderMention.click();
  await expect(codeChatInput.locator("[data-chat-file-mention]")).toHaveAttribute("data-workspace-kind", "directory");
  await page.getByRole("button", { name: "Close chat" }).click();
  await expect(page.locator("[data-code-chat-dock]")).toBeHidden();
  await expect(codeChatToggleControl).toBeFocused();
  await page.waitForTimeout(500);
  await page.reload();
  await expect(page.locator("[data-code-chat-dock]")).toBeHidden();
  await expect(page.locator("[data-code-chat-toggle]")).toHaveAttribute("aria-expanded", "false");
  await page.getByRole("button", { name: "Open code assistant" }).click();
  await expect(page.getByRole("separator", { name: "Resize Code Chat" })).toHaveAttribute("aria-valuenow", String(widthAfter));
  const restoredResources = page.locator(".chat-message-user .chat-prompt-resources").last();
  await expect(restoredResources).toBeVisible();
  await restoredResources.locator("summary").click();
  await expect(restoredResources).toHaveAttribute("open", "");
  await expect(restoredResources).toContainText("main.go");
  await restoredResources.locator(".chat-prompt-resource-row.is-selection").click();
  await expect(page.locator(".code-tab.is-active")).toContainText("main.go");
  await expect(page.getByRole("textbox", { name: "Editor content" })).toBeFocused();
  await expect(selectedContextNotice).toHaveText("Selected context: main.go, line 1 will be included.");

  // Stream following stays at the tail until an upward wheel gesture hands
  // control back to the user, then resumes only after End reaches the bottom.
  const codeChatLog = page.locator("[data-code-chat-dock] [data-chat-log]");
  await codeChatLog.focus();
  await page.keyboard.press("End");
  await expect.poll(() => codeChatLog.evaluate((element) => element.scrollHeight - element.clientHeight - element.scrollTop)).toBeLessThanOrEqual(2);
  await codeChatInput.fill("scroll-follow-e2e");
  await page.keyboard.press("Enter");
  await expect(page.locator(".code-chat-surface .chat-progress-text").last()).toContainText("Scroll follow first 100");
  await expect.poll(() => codeChatLog.evaluate((element) => element.scrollHeight - element.clientHeight - element.scrollTop)).toBeLessThanOrEqual(2);

  await codeChatLog.hover();
  await page.mouse.wheel(0, -480);
  await expect.poll(() => codeChatLog.evaluate((element) => element.scrollHeight - element.clientHeight - element.scrollTop)).toBeGreaterThan(20);
  const manualScrollTop = await codeChatLog.evaluate((element) => element.scrollTop);
  expect(releaseScrollStreamChunk).not.toBeNull();
  releaseScrollStreamChunk?.();
  await expect(page.locator(".code-chat-surface .chat-progress-text").last()).toContainText("Scroll follow manual 30");
  await expect.poll(() => codeChatLog.evaluate((element) => element.scrollTop)).toBe(manualScrollTop);

  await codeChatLog.focus();
  await page.keyboard.press("End");
  await expect.poll(() => codeChatLog.evaluate((element) => element.scrollHeight - element.clientHeight - element.scrollTop)).toBeLessThanOrEqual(2);
  expect(releaseScrollStreamChunk).not.toBeNull();
  releaseScrollStreamChunk?.();
  await expect(page.locator(".code-chat-surface .chat-final-content").last()).toContainText("Scroll follow resumed at the tail.");
  await expect(page.locator(".code-chat-surface .chat-message-assistant.is-streaming")).toHaveCount(0);
  await expect.poll(() => codeChatLog.evaluate((element) => element.scrollHeight - element.clientHeight - element.scrollTop)).toBeLessThanOrEqual(2);
  await page.getByRole("button", { name: "Close chat" }).click();

  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+End");
  await page.keyboard.press("Enter");
  await page.keyboard.type("// saved by Playwright");
  await page.keyboard.press("Control+s");
  await expect.poll(() => readFileSync(mainPath, "utf8")).toContain("saved by Playwright");
  await expect(mainTab.locator(".code-tab-dirty")).not.toHaveClass(/is-visible/);

  writeFileSync(mainPath, "package main\n\n// external reload\nfunc main() {}\n", "utf8");
  await expect(page.locator(".view-lines")).toContainText("external reload");

  await page.getByRole("button", { name: "Explorer", exact: true }).click();
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
  await page.getByRole("button", { name: "Source Control", exact: true }).click();
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
  await expect(page.locator(".code-tab.is-active")).toContainText("main.go (Index)");
  const readOnlyModified = page.locator("[data-monaco-diff-host] .modified-in-monaco-diff-editor .view-lines");
  await expect(readOnlyModified).toBeVisible();
  const readOnlyText = await readOnlyModified.textContent();
  await readOnlyModified.click();
  await page.keyboard.press("Control+a");
  await chooseCaseTransform("Transform to Uppercase");
  await expect(readOnlyModified).toHaveText(readOnlyText!);
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
  await page.getByRole("button", { name: "Source Control", exact: true }).click();
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
  const mobileCodeChatToggle = page.getByRole("button", { name: "Open code assistant" });
  await mobileCodeChatToggle.click();
  await expect(page.locator(".code-app-shell")).toHaveClass(/is-code-chat-open/);
  await expect(page.locator("[data-code-chat-backdrop]")).toBeVisible();
  await page.locator("[data-code-chat-backdrop]").click({ position: { x: 5, y: 5 } });
  await expect(page.locator(".code-app-shell")).not.toHaveClass(/is-code-chat-open/);
  await expect(mobileCodeChatToggle).toBeFocused();
  await mobileCodeChatToggle.click();
  await page.keyboard.press("Escape");
  await expect(page.locator(".code-app-shell")).not.toHaveClass(/is-code-chat-open/);
  await expect(mobileCodeChatToggle).toBeFocused();

  await mobileNav.getByRole("button", { name: "Source Control", exact: true }).click();
  await expect(page).toHaveURL(/#\/code\?sidebar=git$/);
  await expect(page.locator(".code-app-shell")).toHaveClass(/is-explorer-open/);
  await expect(page.getByText("SOURCE CONTROL", { exact: true })).toBeVisible();
  await expect(mobileNav.getByRole("button", { name: "Source Control", exact: true })).toHaveAttribute("aria-current", "page");

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

  await mobileNav.getByRole("button", { name: "Chat", exact: true }).click();
  await expect(page.locator(".app-shell")).toBeVisible();
  await page.setViewportSize({ width: 320, height: 700 });
  mobileNav = page.locator("[data-mobile-primary-nav]");
  await expect(page.locator(".code-toast", { hasText: "InstantiationService has been disposed" })).toHaveCount(0);
  await page.locator(".code-toast-close").evaluateAll((buttons) => buttons.forEach((button) => (button as HTMLButtonElement).click()));
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

test("reorders code editor tabs and restores their persisted order", async ({ page }) => {
  test.setTimeout(120_000);
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8")) as {
    setupCode: string;
    workspace: string;
  };
  const tabWorkspace = join(dirname(state.workspace), "tab-order-workspace");
  mkdirSync(tabWorkspace, { recursive: true });
  for (let index = 0; index < 18; index++) {
    writeFileSync(join(tabWorkspace, `tab-${String(index).padStart(2, "0")}.ts`), `export const tabIndex = ${index};\n`, "utf8");
  }

  await page.goto("/");
  if (await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible()) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Confirm password").fill(password);
    await page.getByLabel("Device name").fill("Playwright Tab Order");
    await page.getByRole("button", { name: "Finish setup" }).click();
  } else {
    await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Device name").fill("Playwright Tab Order");
    await page.getByRole("button", { name: "Sign in" }).click();
  }
  await expect(page.locator(".app-shell")).toBeVisible();

  const previousWorkspaceId = await page.evaluate(async ({ workspacePath, baselinePath }) => {
    const request = async (path: string, method: string, body: unknown) => {
      const response = await fetch(path, {
        method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
      });
      const payload = await response.json();
      if (!response.ok || payload.ok === false) throw new Error(payload.error || `HTTP ${response.status}`);
      return payload.data;
    };
    const currentResponse = await fetch("/api/workspaces");
    const currentPayload = await currentResponse.json();
    let previousActiveId = currentPayload.data?.activeId as string | null;
    if (!previousActiveId) {
      const baseline = await request("/api/workspaces", "POST", { name: "E2E Workspace", mainPath: baselinePath, folders: [] });
      previousActiveId = baseline.workspace.id as string;
    }
    const created = await request("/api/workspaces", "POST", { name: "Tab Order Workspace", mainPath: workspacePath, folders: [] });
    await request("/api/workspaces/active", "PUT", { id: created.workspace.id });
    return previousActiveId;
  }, { workspacePath: tabWorkspace, baselinePath: state.workspace });

  try {
    await page.goto("/#/code");
    await expect(page.locator(".code-app-shell")).toBeVisible();
    const openPinnedTab = async (name: string) => {
      const row = page.locator(".code-tree-label", { hasText: name });
      await row.click();
      const tab = page.getByRole("tab", { name: new RegExp(name.replace(".", "\\.")) });
      await expect(tab).toBeVisible();
      await tab.dblclick();
    };
    await openPinnedTab("tab-00.ts");
    await openPinnedTab("tab-01.ts");
    await openPinnedTab("tab-02.ts");

    const tabTitles = page.locator("[data-tabs-list] > .code-tab > .code-tab-title");
    const firstTab = page.getByRole("tab", { name: /tab-00\.ts/ });
    const activeTab = page.getByRole("tab", { name: /tab-01\.ts/ });
    const lastTab = page.getByRole("tab", { name: /tab-02\.ts/ });
    await activeTab.click();
    await expect(page.locator(".view-lines")).toContainText("tabIndex = 1");

    await dragTabTo(page, firstTab, lastTab, "after");
    await expect(tabTitles).toHaveText(["tab-01.ts", "tab-02.ts", "tab-00.ts"]);
    await expect(activeTab).toHaveAttribute("aria-selected", "true");
    await expect(page.locator(".view-lines")).toContainText("tabIndex = 1");
    await expect(page.locator(".code-tab.is-dragging, .code-tab.is-drop-before, .code-tab.is-drop-after")).toHaveCount(0);

    await page.waitForTimeout(900);
    await page.reload();
    await expect(tabTitles).toHaveText(["tab-01.ts", "tab-02.ts", "tab-00.ts"]);
    await expect(activeTab).toHaveAttribute("aria-selected", "true");
    await expect(page.locator(".view-lines")).toContainText("tabIndex = 1");

    for (let index = 3; index < 18; index++) await openPinnedTab(`tab-${String(index).padStart(2, "0")}.ts`);
    const tabScroller = page.locator("[data-code-tabs]");
    await expect.poll(() => tabScroller.evaluate((element) => element.scrollWidth > element.clientWidth)).toBe(true);
    await tabScroller.evaluate((element) => { element.scrollLeft = 0; });
    const overflowSource = page.getByRole("tab", { name: /tab-01\.ts/ });
    const sourceBox = await overflowSource.boundingBox();
    const scrollerBox = await tabScroller.boundingBox();
    expect(sourceBox).not.toBeNull();
    expect(scrollerBox).not.toBeNull();
    await page.mouse.move(sourceBox!.x + sourceBox!.width / 2, sourceBox!.y + sourceBox!.height / 2);
    await page.mouse.down();
    try {
      await page.mouse.move(sourceBox!.x + sourceBox!.width / 2 + 8, sourceBox!.y + sourceBox!.height / 2, { steps: 2 });
      await page.mouse.move(scrollerBox!.x + scrollerBox!.width - 8, scrollerBox!.y + scrollerBox!.height / 2, { steps: 8 });
      await expect.poll(() => tabScroller.evaluate((element) => element.scrollLeft)).toBeGreaterThan(0);
      const firstScrollLeft = await tabScroller.evaluate((element) => element.scrollLeft);
      await page.waitForTimeout(250);
      await expect.poll(() => tabScroller.evaluate((element) => element.scrollLeft)).toBeGreaterThan(firstScrollLeft);
    } finally {
      await page.mouse.up();
    }
    await expect(page.locator(".code-tab.is-dragging, .code-tab.is-drop-before, .code-tab.is-drop-after")).toHaveCount(0);
  } finally {
    if (previousWorkspaceId) {
      await page.evaluate(async (id) => {
        const response = await fetch("/api/workspaces/active", {
          method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id }),
        });
        if (!response.ok) throw new Error(`Could not restore active workspace: HTTP ${response.status}`);
      }, previousWorkspaceId);
    }
  }
});

test("drags an open explorer file into another folder", async ({ page }) => {
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8")) as {
    setupCode: string;
    workspace: string;
  };
  const dragWorkspace = join(dirname(state.workspace), "drag-workspace");
  mkdirSync(join(dragWorkspace, "source"), { recursive: true });
  mkdirSync(join(dragWorkspace, "target"), { recursive: true });
  writeFileSync(join(dragWorkspace, "source", "move-me.ts"), "export const moved = true;\n", "utf8");

  await page.goto("/");
  if (await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible()) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Confirm password").fill(password);
    await page.getByLabel("Device name").fill("Playwright Drag Drop");
    await page.getByRole("button", { name: "Finish setup" }).click();
  } else {
    await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Device name").fill("Playwright Drag Drop");
    await page.getByRole("button", { name: "Sign in" }).click();
  }
  await expect(page.locator(".app-shell")).toBeVisible();

  const previousWorkspaceId = await page.evaluate(async ({ workspacePath, baselinePath }) => {
    const request = async (path: string, method: string, body: unknown) => {
      const response = await fetch(path, {
        method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
      });
      const payload = await response.json();
      if (!response.ok || payload.ok === false) throw new Error(payload.error || `HTTP ${response.status}`);
      return payload.data;
    };
    const currentResponse = await fetch("/api/workspaces");
    const currentPayload = await currentResponse.json();
    let previousActiveId = currentPayload.data?.activeId as string | null;
    if (!previousActiveId) {
      const baseline = await request("/api/workspaces", "POST", { name: "E2E Workspace", mainPath: baselinePath, folders: [] });
      previousActiveId = baseline.workspace.id as string;
      await request("/api/workspaces/active", "PUT", { id: previousActiveId });
    }
    const created = await request("/api/workspaces", "POST", { name: "Drag Workspace", mainPath: workspacePath, folders: [] });
    await request("/api/workspaces/active", "PUT", { id: created.workspace.id });
    return previousActiveId;
  }, { workspacePath: dragWorkspace, baselinePath: state.workspace });

  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toBeVisible();
  await page.locator(".code-tree-row", { hasText: "source" }).click();
  const source = page.locator(".code-tree-row", { hasText: "move-me.ts" });
  await source.click();
  await expect(page.locator(".code-tab.is-active", { hasText: "move-me.ts" })).toHaveAttribute("aria-selected", "true");
  await dragToTreeRow(page, source, page.locator(".code-tree-row", { hasText: "target" }));

  await expect.poll(() => existsSync(join(dragWorkspace, "target", "move-me.ts"))).toBe(true);
  await expect.poll(() => existsSync(join(dragWorkspace, "source", "move-me.ts"))).toBe(false);
  await expect(page.getByRole("tab", { name: /move-me\.ts/ })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".view-lines")).toContainText("export const moved = true");
  await expect(page.locator(".code-tree-row", { hasText: "move-me.ts" })).toHaveAttribute("aria-selected", "true");
  if (previousWorkspaceId) {
    await page.evaluate(async (id) => {
      const response = await fetch("/api/workspaces/active", {
        method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id }),
      });
      if (!response.ok) throw new Error(`Could not restore active workspace: HTTP ${response.status}`);
    }, previousWorkspaceId);
  }
});

test("auto-scrolls the explorer while dragging near the bottom edge", async ({ page }) => {
  test.setTimeout(120_000);
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8")) as {
    setupCode: string;
    workspace: string;
  };
  const scrollWorkspace = join(dirname(state.workspace), "scroll-workspace");
  mkdirSync(scrollWorkspace, { recursive: true });
  // Enough rows (22px each) to overflow the explorer and make scrolling meaningful.
  for (let i = 0; i < 60; i++) {
    writeFileSync(join(scrollWorkspace, `file-${String(i).padStart(2, "0")}.txt`), `file ${i}\n`, "utf8");
  }

  await page.goto("/");
  if (await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible()) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Confirm password").fill(password);
    await page.getByLabel("Device name").fill("Playwright Scroll");
    await page.getByRole("button", { name: "Finish setup" }).click();
  } else {
    await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Device name").fill("Playwright Scroll");
    await page.getByRole("button", { name: "Sign in" }).click();
  }
  await expect(page.locator(".app-shell")).toBeVisible();

  const previousWorkspaceId = await page.evaluate(async ({ workspacePath, baselinePath }) => {
    const request = async (path: string, method: string, body: unknown) => {
      const response = await fetch(path, {
        method, headers: { "Content-Type": "application/json" }, body: JSON.stringify(body),
      });
      const payload = await response.json();
      if (!response.ok || payload.ok === false) throw new Error(payload.error || `HTTP ${response.status}`);
      return payload.data;
    };
    const currentResponse = await fetch("/api/workspaces");
    const currentPayload = await currentResponse.json();
    let previousActiveId = currentPayload.data?.activeId as string | null;
    if (!previousActiveId) {
      const baseline = await request("/api/workspaces", "POST", { name: "E2E Workspace", mainPath: baselinePath, folders: [] });
      previousActiveId = baseline.workspace.id as string;
      await request("/api/workspaces/active", "PUT", { id: previousActiveId });
    }
    const created = await request("/api/workspaces", "POST", { name: "Scroll Workspace", mainPath: workspacePath, folders: [] });
    await request("/api/workspaces/active", "PUT", { id: created.workspace.id });
    return previousActiveId;
  }, { workspacePath: scrollWorkspace, baselinePath: state.workspace });

  await page.goto("/#/code");
  await expect(page.locator(".code-app-shell")).toBeVisible();

  const tree = page.locator("[data-code-tree]");
  const firstFile = page.locator(".code-tree-row", { hasText: "file-00.txt" });
  await expect(firstFile).toBeVisible();

  const treeBox = await tree.boundingBox();
  expect(treeBox).not.toBeNull();
  const scrollable = await tree.evaluate((element) => element.scrollHeight > element.clientHeight);
  expect(scrollable).toBe(true);

  const sourceBox = await firstFile.boundingBox();
  expect(sourceBox).not.toBeNull();

  // Begin dragging the first file, then hold it just inside the bottom edge.
  await page.mouse.move(sourceBox!.x + sourceBox!.width / 2, sourceBox!.y + sourceBox!.height / 2);
  await page.mouse.down();
  await page.mouse.move(sourceBox!.x + sourceBox!.width / 2 + 8, sourceBox!.y + sourceBox!.height / 2, { steps: 2 });
  const bottomEdgeY = treeBox!.y + treeBox!.height - 10;
  await page.mouse.move(sourceBox!.x + sourceBox!.width / 2, bottomEdgeY, { steps: 8 });

  // The rAF auto-scroll should keep advancing while the cursor rests in the edge zone.
  await expect.poll(() => tree.evaluate((element) => element.scrollTop)).toBeGreaterThan(0);
  const scrollTopAfter = await tree.evaluate((element) => element.scrollTop);
  await page.waitForTimeout(300);
  const scrollTopLater = await tree.evaluate((element) => element.scrollTop);
  expect(scrollTopLater).toBeGreaterThan(scrollTopAfter);

  await page.mouse.up();

  if (previousWorkspaceId) {
    await page.evaluate(async (id) => {
      const response = await fetch("/api/workspaces/active", {
        method: "PUT", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id }),
      });
      if (!response.ok) throw new Error(`Could not restore active workspace: HTTP ${response.status}`);
    }, previousWorkspaceId);
  }
});

test("runs the deterministic fake language server through Monaco and settings", async ({ page }) => {
  test.setTimeout(120_000);
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8")) as {
    setupCode: string;
    workspace: string;
    nodePath: string;
    fakeLSPPath: string;
  };
  writeFileSync(join(state.workspace, "main.go"), "package main\n\nfunc main() {\n\tTarget()\n}\n", "utf8");
  writeFileSync(join(state.workspace, "definition.go"), "package main\n\nfunc Target() {}\n", "utf8");
  writeFileSync(join(state.workspace, "usage.go"), "package main\n\nfunc useTarget() {\n\tTarget()\n}\n", "utf8");
  await page.goto("/");
  if (await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible()) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Confirm password").fill(password);
    await page.getByLabel("Device name").fill("Playwright LSP");
    await page.getByRole("button", { name: "Finish setup" }).click();
  } else {
    await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Device name").fill("Playwright LSP");
    await page.getByRole("button", { name: "Sign in" }).click();
  }
  await expect(page.locator(".app-shell")).toBeVisible();

  const workspaceId = await page.evaluate(async ({ command, script, workspacePath }) => {
    const request = async (path: string, method = "GET", body?: unknown) => {
      const response = await fetch(path, {
        method, headers: body === undefined ? undefined : { "Content-Type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const payload = await response.json();
      if (!response.ok || payload.ok === false) throw new Error(payload.error || `HTTP ${response.status}`);
      return payload.data;
    };
    let workspaces = await request("/api/workspaces");
    if (!workspaces.activeId) {
      const created = await request("/api/workspaces", "POST", { name: "LSP E2E Workspace", mainPath: workspacePath, folders: [] });
      await request("/api/workspaces/active", "PUT", { id: created.workspace.id });
      workspaces = await request("/api/workspaces");
    }
    await request("/api/lsp/profiles", "POST", { profile: {
      id: "fake-e2e-lsp", name: "Fake E2E LSP", command, args: [script],
      selectors: [{ languageId: "go", extensions: [".go"] }],
      environment: { ECHO_FAKE_LSP_LOG: `${workspacePath}/.echo/fake-lsp.log` },
      initializationOptions: { deterministic: true }, settings: { fake: { enabled: true } },
    } });
    await request(`/api/workspaces/${encodeURIComponent(workspaces.activeId)}/lsp/config`, "PUT", { config: {
      enabledProfileIds: ["fake-e2e-lsp"], formatOnSave: false, formatOnSaveTimeoutMs: 3000,
    } });
    return workspaces.activeId as string;
  }, { command: state.nodePath, script: state.fakeLSPPath, workspacePath: state.workspace });

  await page.goto("/#/code");
  await expect(page.locator(".code-tree-label", { hasText: "usage.go" })).toBeVisible();
  await page.locator(".code-tree-label", { hasText: "usage.go" }).click();
  const lspStatus = page.locator('[data-status="lsp"]');
  await expect(lspStatus).toContainText("Fake E2E LSP ✓", { timeout: 20_000 });
  await expect(page.locator(".squiggly-error").first()).toBeVisible();

  // Direct navigation activates only the selected target tab, attaches its
  // model, and applies Monaco's returned cursor position.
  await page.locator(".view-line", { hasText: /^\s*Target\(\)\s*$/ }).click();
  const usageCursor = await page.locator('[data-status="cursor"]').textContent();
  await page.keyboard.press("F12");
  await expect(page.locator(".code-tab.is-active")).toContainText("definition.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 3, Col 6");
  await expect(page.locator("[data-tabs-list] .code-tab")).toHaveCount(1);
  await expect(lspStatus).toHaveAttribute("data-lsp-state", "owned");

  await page.getByRole("button", { name: "Search", exact: true }).click();
  await expect(page).toHaveURL(/#\/code\?sidebar=search$/);
  await page.locator(".code-app-shell").evaluate((element) => { element.dataset.historyMount = "stable"; });

  // Keyboard and browser traversal restore both sides of the F12 jump.
  await page.keyboard.press("Alt+ArrowLeft");
  await expect(page).toHaveURL(/#\/code$/);
  await expect(page.locator(".code-app-shell")).toHaveAttribute("data-history-mount", "stable");
  await expect(page.locator(".code-tab.is-active")).toContainText("usage.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText(usageCursor!);
  await page.keyboard.press("Alt+ArrowRight");
  await expect(page).toHaveURL(/#\/code\?sidebar=search$/);
  await expect(page.locator(".code-app-shell")).toHaveAttribute("data-history-mount", "stable");
  await expect(page.locator(".code-tab.is-active")).toContainText("definition.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 3, Col 6");
  await page.goBack();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("data-history-mount", "stable");
  await expect(page.locator(".code-tab.is-active")).toContainText("usage.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText(usageCursor!);
  await page.goForward();
  await expect(page.locator(".code-app-shell")).toHaveAttribute("data-history-mount", "stable");
  await expect(page.locator(".code-tab.is-active")).toContainText("definition.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 3, Col 6");

  // A closed destination is reopened when browser history returns to it.
  await page.getByRole("button", { name: "Close definition.go" }).click();
  await expect(page.locator("[data-tabs-list] .code-tab")).toHaveCount(0);
  await page.keyboard.press("Alt+ArrowLeft");
  await expect(page.locator(".code-tab.is-active")).toContainText("usage.go");
  await page.keyboard.press("Alt+ArrowRight");
  await expect(page.locator(".code-tab.is-active")).toContainText("definition.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 3, Col 6");
  await page.getByRole("button", { name: "Explorer", exact: true }).click();
  await page.getByRole("textbox", { name: "Editor content" }).focus();

  // F12 at the definition and the explicit peek shortcuts render Monaco's
  // native reference zone without turning every result into an Echo tab.
  await page.keyboard.press("F12");
  await expect(page.locator(".reference-zone-widget")).toBeVisible();
  await expect(page.locator("[data-tabs-list] .code-tab")).toHaveCount(1);
  await page.keyboard.press("Escape");
  await expect(page.locator(".reference-zone-widget")).toHaveCount(0);

  await page.keyboard.press("Shift+F12");
  await expect(page.locator(".reference-zone-widget")).toBeVisible();
  await expect(page.locator("[data-tabs-list] .code-tab")).toHaveCount(1);
  const referenceFiles = page.locator(".reference-zone-widget .reference-file");
  await expect(referenceFiles).toHaveCount(3);

  // Moving from a file group into a reference updates the preview without
  // committing a navigation away from the source editor.
  await referenceFiles.filter({ hasText: "main.go" }).click();
  await page.keyboard.press("ArrowDown");
  await expect(page.locator(".reference-zone-widget .monaco-list-row.focused .referenceMatch")).toContainText("Target()");
  await expect(page.locator(".reference-zone-widget .preview")).toContainText("func main");
  await expect(page.locator(".code-tab.is-active")).toContainText("definition.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 3, Col 6");
  await expect(page.locator("[data-tabs-list] .code-tab")).toHaveCount(1);

  // Enter commits the focused reference through Echo's editor opener.
  await page.keyboard.press("Enter");
  await expect(page.locator(".reference-zone-widget")).toHaveCount(0);
  await expect(page.locator(".code-tab.is-active")).toContainText("main.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 4, Col 2");
  await expect(page.getByRole("textbox", { name: "Editor content" })).toBeFocused();
  await expect(page.locator("[data-tabs-list] .code-tab")).toHaveCount(1);

  await page.keyboard.press("Alt+ArrowLeft");
  await expect(page.locator(".code-tab.is-active")).toContainText("definition.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 3, Col 6");
  await page.keyboard.press("Alt+ArrowRight");
  await expect(page.locator(".code-tab.is-active")).toContainText("main.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 4, Col 2");

  // Double-clicking a result in an explicit peek commits exactly like Enter.
  await page.keyboard.press("Alt+F12");
  await expect(page.locator(".reference-zone-widget")).toBeVisible();
  await page.locator(".reference-zone-widget .referenceMatch").dblclick();
  await expect(page.locator(".reference-zone-widget")).toHaveCount(0);
  await expect(page.locator(".code-tab.is-active")).toContainText("definition.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 3, Col 6");
  await expect(page.getByRole("textbox", { name: "Editor content" })).toBeFocused();

  await page.keyboard.press("Control+Shift+F12");
  await expect(page.locator(".reference-zone-widget")).toBeVisible();
  await page.keyboard.press("Escape");

  await page.keyboard.press("Control+F12");
  await expect(page.locator(".code-tab.is-active")).toContainText("implementation.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 9, Col 23");

  await page.keyboard.press("Control+Shift+O");
  const workspaceSymbols = page.getByLabel("Workspace Symbol Search");
  await expect(workspaceSymbols).toBeFocused();
  await workspaceSymbols.fill("Target");
  await expect(page.getByRole("option", { name: /Target/ })).toBeVisible();
  await page.keyboard.press("Enter");
  await expect(page.locator(".code-tab.is-active")).toContainText("definition.go");
  await expect(page.locator('[data-status="cursor"]')).toHaveText("Ln 3, Col 6");

  for (const command of [
    "Go to Definition", "Peek Definition", "Go to Declaration", "Peek Declaration",
    "Go to Type Definition", "Peek Type Definition", "Go to Implementations",
    "Peek Implementations", "Peek References", "Go to Symbol in Workspace…",
  ]) {
    await page.keyboard.press("Control+Shift+P");
    const palette = page.getByLabel("Command Palette");
    await palette.fill(command);
    await expect(page.getByRole("option", { name: new RegExp(command.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")) }).first()).toBeVisible();
    await page.keyboard.press("Escape");
  }
  await page.keyboard.press("Control+Shift+P");
  await page.getByLabel("Command Palette").fill("Go to Symbol in Workspace");
  await expect(page.getByRole("option", { name: /Go to Symbol in Workspace/ })).toContainText("Ctrl+Shift+O");
  await expect(page.getByRole("option", { name: /Go to Symbol in Workspace/ })).not.toContainText("Ctrl+T");
  await page.keyboard.press("Escape");

  // A single cursor move of at least ten lines becomes a navigation entry.
  await page.locator(".code-tree-label", { hasText: "navigation.go" }).click();
  await page.locator(".view-line", { hasText: "navigation line 02" }).click();
  const nearCursor = await page.locator('[data-status="cursor"]').textContent();
  await page.locator(".view-line", { hasText: "navigation line 25" }).click();
  const farCursor = await page.locator('[data-status="cursor"]').textContent();
  await page.keyboard.press("Alt+ArrowLeft");
  await expect(page.locator('[data-status="cursor"]')).toHaveText(nearCursor!);
  await page.keyboard.press("Alt+ArrowRight");
  await expect(page.locator('[data-status="cursor"]')).toHaveText(farCursor!);
  await page.keyboard.press("Alt+ArrowLeft");
  await page.locator(".view-line", { hasText: "navigation line 15" }).click();
  const branchedCursor = await page.locator('[data-status="cursor"]').textContent();
  expect(await page.goForward()).toBeNull();
  await expect(page.locator('[data-status="cursor"]')).toHaveText(branchedCursor!);

  // Continue exercising the other providers on the original document.
  await page.locator(".code-tree-label", { hasText: "main.go" }).click();
  await expect(lspStatus).toHaveAttribute("data-lsp-state", "owned");
  const functionLine = page.locator(".view-line", { hasText: "func main" });
  await functionLine.click({ position: { x: 62, y: 10 } });
  await page.keyboard.press("Control+Shift+p");
  await page.getByLabel("Command Palette").fill("Editor: Show Hover");
  await page.keyboard.press("Enter");
  await expect(page.locator(".monaco-hover:not(.hidden)")).toContainText("Echo fake hover");

  await page.locator(".view-lines").click();
  await page.keyboard.press("Control+End");
  await page.keyboard.press("Enter");
  await page.keyboard.type("fak");
  await page.keyboard.press("Control+Space");
  await expect(page.locator(".suggest-widget")).toContainText("fakeCompletion");
  await page.keyboard.press("Enter");
  await expect(page.locator(".view-lines")).toContainText("fakeCompletion");

  // The fake server also edits the currently closed definition.go. Preparing
  // that model must not switch Monaco's active model while this request runs.
  await page.keyboard.press("F2");
  const rename = page.locator(".rename-box input");
  await expect(rename).toBeVisible();
  await rename.fill("renamedMain");
  await rename.press("Enter");
  await expect(page.locator(".view-lines")).toContainText("renamedMain");
  await expect(page.locator(".code-tab.is-active")).toContainText("main.go");
  await expect(page.getByRole("textbox", { name: "Editor content" })).toBeFocused();
  await expect(page.locator(".code-tab", { hasText: "definition.go" }).locator(".code-tab-dirty")).toHaveClass(/is-visible/);

  await page.keyboard.press("Shift+Alt+f");
  await expect(page.locator(".view-lines")).toContainText("formatted by Echo fake LSP");

  const persisted = await page.evaluate(async (id) => {
    const response = await fetch(`/api/workspaces/${encodeURIComponent(id)}/lsp/config`);
    return (await response.json()).data;
  }, workspaceId);
  expect(persisted.config.enabledProfileIds).toEqual(["fake-e2e-lsp"]);
  expect(persisted.config.formatOnSaveTimeoutMs).toBe(3000);

  // Once Code's location entries are exhausted, Back leaves the route and
  // Forward re-enters at the latest file and caret in this browser session.
  const boundary = await page.evaluate(() => ({
    steps: Number(history.state?.echoCodeNavigation?.sequence || 0) + 1,
    cursor: document.querySelector('[data-status="cursor"]')?.textContent || "",
    tab: document.querySelector(".code-tab.is-active .code-tab-title")?.textContent || "",
  }));
  await page.evaluate((steps) => history.go(-steps), boundary.steps);
  await expect(page).not.toHaveURL(/#\/code/);
  await expect(page.locator(".app-shell")).toBeVisible();
  await page.evaluate((steps) => history.go(steps), boundary.steps);
  await expect(page).toHaveURL(/#\/code/);
  await expect(page.locator(".code-tab.is-active")).toContainText(boundary.tab);
  await expect(page.locator('[data-status="cursor"]')).toHaveText(boundary.cursor);

  await page.goto("/#/settings?section=lsp");
  await expect(page.getByRole("heading", { name: "Language Servers" })).toBeVisible();
  await expect(page.locator('[data-lsp-enable="fake-e2e-lsp"]')).toBeChecked();
  await expect(page.locator(".lsp-runtime-card", { hasText: "Fake E2E LSP" })).toContainText("running");
  await page.locator('[data-lsp-action="restart"][data-profile-id="fake-e2e-lsp"]').click();
  await expect.poll(async () => page.evaluate(async (id) => {
    const response = await fetch(`/api/workspaces/${encodeURIComponent(id)}/lsp/config`);
    const data = (await response.json()).data;
    return data.statuses.find((status: { profileId: string }) => status.profileId === "fake-e2e-lsp")?.state;
  }, workspaceId)).toBe("running");
});

test("debugs through a deterministic DAP adapter and reconnects the workbench", async ({ page }) => {
  test.setTimeout(120_000);
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8")) as {
    setupCode: string;
    workspace: string;
    nodePath: string;
  };
  const fakeDAPPath = resolve(directory, "fake-dap.mjs");
  writeFileSync(join(state.workspace, "main.go"), "package main\n\nfunc main() {\n\tTarget()\n}\n", "utf8");
  writeFileSync(join(state.workspace, "sample_test.go"), `package main

import (
	"testing"
	"time"
)

func TestPass(t *testing.T) {}

func TestSubtests(t *testing.T) {
	t.Run("nested name", func(child *testing.T) {})
}

func TestFail(t *testing.T) {
	t.Fatal("intentional failure")
}

func TestSlow(t *testing.T) {
	time.Sleep(5 * time.Second)
}
`, "utf8");

  await page.goto("/");
  if (await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible()) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Confirm password").fill(password);
    await page.getByLabel("Device name").fill("Playwright DAP");
    await page.getByRole("button", { name: "Finish setup" }).click();
  } else {
    await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Device name").fill("Playwright DAP");
    await page.getByRole("button", { name: "Sign in" }).click();
  }
  await expect(page.locator(".app-shell")).toBeVisible();

  const workspaceId = await page.evaluate(async ({ command, script, workspacePath }) => {
    const request = async (path: string, method = "GET", body?: unknown) => {
      const response = await fetch(path, {
        method, headers: body === undefined ? undefined : { "Content-Type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const payload = await response.json();
      if (!response.ok || payload.ok === false) throw new Error(payload.error || `HTTP ${response.status}`);
      return payload.data;
    };
    const workspaceData = await request("/api/workspaces");
    let workspace = (workspaceData.workspaces || []).find((candidate: { mainPath: string }) => candidate.mainPath === workspacePath);
    if (!workspace) workspace = (await request("/api/workspaces", "POST", { name: "DAP E2E Workspace", mainPath: workspacePath, folders: [] })).workspace;
    await request("/api/workspaces/active", "PUT", { id: workspace.id });
    const profiles = await request("/api/debug/adapter-profiles");
    if (!(profiles.profiles || []).some((profile: { id: string }) => profile.id === "fake-e2e-dap")) {
      await request("/api/debug/adapter-profiles", "POST", { profile: {
        id: "fake-e2e-dap", name: "Fake E2E DAP", adapterId: "go",
        command, args: [script], environment: {}, selectors: [{ languageId: "go", extensions: [".go"] }],
        transport: { kind: "stdio", startupTimeoutMs: 15000 },
      } });
    }
    await request(`/api/workspaces/${encodeURIComponent(workspace.id)}/debug/config`, "PUT", {
      version: 1, enabledAdapterProfileIds: ["fake-e2e-dap"], overrides: {},
      configurations: [{
        id: "fake-main", name: "Fake: Main", adapterProfileId: "fake-e2e-dap", request: "launch",
        arguments: { program: "${file}" },
      }], compounds: [], inputs: [],
    });
    return workspace.id as string;
  }, { command: state.nodePath, script: fakeDAPPath, workspacePath: state.workspace });

  await page.goto("/#/code");
  await expect(page.locator(".code-tree-label", { hasText: "main.go" })).toBeVisible();
  await page.locator(".code-tree-label", { hasText: "main.go" }).click();
  await page.locator(".view-line", { hasText: "Target()" }).click();
  await page.keyboard.press("F9");
  await expect(page.locator(".cgmr.echo-debug-breakpoint")).toBeVisible();

  await page.keyboard.press("Control+5");
  await expect(page.getByLabel("Debug configuration")).toHaveValue("fake-main");
  await page.keyboard.press("F5");
  await expect(page.locator(".debug-session-row .debug-status.is-stopped")).toBeVisible({ timeout: 20_000 });
  await expect(page.locator(".debug-variable", { hasText: "x" }).first()).toContainText("42");
  await expect(page.getByRole("button", { name: /^main main\.go:/ })).toBeVisible();

  await page.getByRole("button", { name: "Add Watch Expression" }).click();
  const watchExpression = page.getByRole("textbox", { name: "Expression", exact: true });
  await watchExpression.fill("x");
  await watchExpression.press("Enter");
  await expect(page.locator(".debug-watch-row", { hasText: "x" })).toContainText("42");

  await page.getByRole("tab", { name: "Debug Console" }).click();
  const repl = page.getByLabel("Debug Console expression");
  await repl.fill("x");
  await repl.press("Enter");
  await expect(page.locator(".debug-console-entry", { hasText: "42" })).toBeVisible();

  await page.locator(".view-lines").click();
  await page.keyboard.press("F10");
  await expect(page.locator(".debug-session-row .debug-status.is-stopped")).toBeVisible();
  await expect(page.getByRole("button", { name: /^main main\.go:/ })).toContainText("5");

  await page.reload();
  await expect(page.locator(".code-app-shell")).toBeVisible();
  await expect(page.locator(".debug-session-row .debug-status.is-stopped")).toBeVisible({ timeout: 20_000 });
  const snapshot = await page.evaluate(async (id) => {
    const response = await fetch(`/api/workspaces/${encodeURIComponent(id)}/debug/snapshot`);
    return (await response.json()).data.snapshot;
  }, workspaceId);
  expect(snapshot.sessions.some((session: { status: string; configuration: string }) => session.status === "stopped" && session.configuration === "Fake: Main")).toBe(true);

  await page.locator(".debug-floating-toolbar [data-debug-action=stop]").click();
  await expect(page.locator(".debug-floating-toolbar")).toHaveCount(0);

  // Built-in Go CodeLens runs without gopls and uses the same bottom workbench
  // and transient DAP session exercised above.
  await page.getByRole("button", { name: "Explorer" }).click();
  await expect(page.locator(".code-tree-label", { hasText: "sample_test.go" })).toBeVisible();
  await page.locator(".code-tree-label", { hasText: "sample_test.go" }).click();
  await expect(page.getByText("run package tests", { exact: true })).toBeVisible();
  await expect(page.getByText("run file tests", { exact: true })).toBeVisible();
  const runLenses = page.getByText("run test", { exact: true });
  const debugLenses = page.getByText("debug test", { exact: true });
  await expect(runLenses).toHaveCount(5);
  await expect(debugLenses).toHaveCount(5);

  await runLenses.first().click();
  await expect(page.getByRole("tab", { name: "Test Output" })).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".go-test-status")).toContainText("Passed", { timeout: 30_000 });
  await expect(page.locator("[data-test-output-text]")).toContainText("go test");

  await runLenses.nth(3).click();
  await expect(page.locator(".go-test-status")).toContainText("Failed", { timeout: 30_000 });
  await expect(page.locator("[data-test-output-text]")).toContainText("intentional failure");

  await page.getByRole("button", { name: "Close terminal" }).click();
  await runLenses.last().click();
  await expect(page.locator(".go-test-status")).toContainText("Running");
  await runLenses.first().click();
  await expect(page.locator(".go-test-status")).toContainText("Passed", { timeout: 30_000 });
  await expect(page.locator("[data-test-output-text]")).toContainText("^TestPass$");

  await debugLenses.first().click();
  await expect(page.locator(".debug-session-row .debug-status.is-stopped")).toBeVisible({ timeout: 20_000 });
  await expect(page.locator(".debug-session-row", { hasText: "Debug Test: TestPass" })).toBeVisible();
  await page.locator(".debug-floating-toolbar [data-debug-action=stop]").click();
});
