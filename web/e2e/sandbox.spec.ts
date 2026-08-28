import { expect, test, type Route } from "@playwright/test";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));
const password = "Echo-E2E-Password!";

test("enables, starts, observes, takes over, reconnects, and resets the sandbox", async ({ page }) => {
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8")) as {
    setupCode: string;
    workspace: string;
  };

  await page.goto("/");
  if (await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible()) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Confirm password").fill(password);
    await page.getByLabel("Device name").fill("Playwright Sandbox");
    await page.getByRole("button", { name: "Finish setup" }).click();
  } else {
    await expect(page.getByRole("heading", { name: "Welcome back" })).toBeVisible();
    await page.getByLabel("Password", { exact: true }).fill(password);
    await page.getByLabel("Device name").fill("Playwright Sandbox");
    await page.getByRole("button", { name: "Sign in" }).click();
  }
  await expect(page.locator(".app-shell")).toBeVisible();

  const workspaceId = await page.evaluate(async (workspacePath) => {
    const request = async (path: string, method = "GET", body?: unknown) => {
      const response = await fetch(path, {
        method,
        headers: body === undefined ? undefined : { "Content-Type": "application/json" },
        body: body === undefined ? undefined : JSON.stringify(body),
      });
      const payload = await response.json();
      if (!response.ok || payload.ok === false) throw new Error(payload.error || `HTTP ${response.status}`);
      return payload.data;
    };
    let registered = await request("/api/workspaces");
    if (!registered.activeId) {
      const created = await request("/api/workspaces", "POST", {
        name: "Sandbox E2E Workspace", mainPath: workspacePath, folders: [],
      });
      await request("/api/workspaces/active", "PUT", { id: created.workspace.id });
      registered = await request("/api/workspaces");
    }
    return registered.activeId as string;
  }, state.workspace);

  let sandboxEnabled = false;
  let sandboxState = "disabled";
  let controlOwner: "none" | "ai" | "user" = "none";
  let leaseRevision = 0;
  let browserResetCalls = 0;
  let desktopSessionCalls = 0;
  let desktopSocketCalls = 0;
  const sandboxConfig = () => ({ enabled: sandboxEnabled, cpuLimit: 4, memoryMiB: 6144, idleTimeoutMinutes: 30 });
  const sandboxStatus = () => ({
    state: sandboxState,
    enabled: sandboxEnabled,
    protocolVersion: "1",
    controlOwner,
    desktopLease: { owner: controlOwner, revision: leaseRevision },
    activeViewers: desktopSessionCalls > 0 ? 1 : 0,
    resources: { memoryBytes: 768 << 20, memoryLimitBytes: 6144 << 20, diskBytes: 256 << 20, activeProcesses: 7 },
    setup: { state: "succeeded" },
  });
  const fulfill = (route: Route, data: unknown, status = 200) => route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify({ ok: status < 400, data }),
  });

  await page.route("**/api/sandbox/host", (route) => fulfill(route, {
    available: true,
    supported: true,
    linuxEngine: true,
    architecture: "amd64",
    operatingSystem: "Docker Desktop",
    serverVersion: "28.0",
    images: {
      workbench: { reference: "workbench@sha256:test", present: true },
      desktop: { reference: "desktop@sha256:test", present: true },
      gateway: { reference: "gateway@sha256:test", present: true },
    },
  }));
  await page.route(`**/api/workspaces/${workspaceId}/sandbox`, async (route) => {
    if (route.request().method() === "PUT") {
      const request = route.request().postDataJSON() as { config: { enabled: boolean } };
      sandboxEnabled = request.config.enabled;
      sandboxState = sandboxEnabled ? "stopped" : "disabled";
    }
    await fulfill(route, { config: sandboxConfig(), status: sandboxStatus() });
  });
  await page.route(`**/api/workspaces/${workspaceId}/sandbox/network-grants**`, (route) => fulfill(route, { grants: [] }));
  await page.route(`**/api/workspaces/${workspaceId}/sandbox/actions`, async (route) => {
    const request = route.request().postDataJSON() as { action: string };
    if (request.action === "start") {
      sandboxState = "ready";
      controlOwner = "ai";
      leaseRevision++;
    }
    if (request.action === "reset_browser") browserResetCalls++;
    await fulfill(route, { result: { action: request.action }, status: sandboxStatus() });
  });
  await page.route(`**/api/workspaces/${workspaceId}/sandbox/desktop-control`, async (route) => {
    const request = route.request().postDataJSON() as { action: "take" | "release" };
    controlOwner = request.action === "take" ? "user" : "none";
    leaseRevision++;
    await fulfill(route, { lease: { owner: controlOwner, revision: leaseRevision } });
  });
  await page.route(`**/api/workspaces/${workspaceId}/sandbox/desktop-sessions**`, async (route) => {
    if (route.request().method() === "POST") {
      desktopSessionCalls++;
      await fulfill(route, { session: {
        id: `desktop-session-${desktopSessionCalls}`,
        credential: "sandbox-vnc-password",
        viewOnly: true,
        expiresAt: new Date(Date.now() + 60_000).toISOString(),
      } }, 201);
      return;
    }
    await fulfill(route, { deleted: true });
  });
  page.on("websocket", (socket) => {
    if (new URL(socket.url()).pathname === `/api/workspaces/${workspaceId}/sandbox/desktop-ws`) desktopSocketCalls++;
  });

  await page.goto("/#/sandbox");
  await expect(page.getByRole("heading", { name: "Linux Sandbox" })).toBeVisible();
  await expect(page.locator("[data-sandbox-state]")).toHaveText("disabled");
  await page.getByRole("button", { name: "Enable sandbox" }).click();
  await expect(page.locator("[data-sandbox-state]")).toHaveText("stopped");
  await page.getByRole("button", { name: "Start" }).click();
  await expect(page.locator("[data-sandbox-state]")).toHaveText("ready");
  await expect(page.locator(".sandbox-lease")).toContainText("AI is controlling");
  await page.getByRole("button", { name: "I understand — open desktop" }).click();

  await expect.poll(() => desktopSocketCalls).toBeGreaterThanOrEqual(1);
  await expect.poll(() => desktopSessionCalls, { timeout: 5_000 }).toBeGreaterThanOrEqual(2);

  await page.getByRole("button", { name: "Take Control" }).click();
  await expect(page.getByRole("button", { name: "Return Control" })).toBeVisible();
  await expect(page.locator(".sandbox-lease")).toContainText("You have control");
  await page.getByRole("button", { name: "Return Control" }).click();
  await expect(page.locator(".sandbox-lease")).toContainText("View only");

  page.once("dialog", (dialog) => dialog.dismiss());
  await page.getByRole("button", { name: "Reset browser data" }).click();
  expect(browserResetCalls).toBe(0);
  page.once("dialog", (dialog) => dialog.accept());
  await page.getByRole("button", { name: "Reset browser data" }).click();
  await expect.poll(() => browserResetCalls).toBe(1);

  await page.setViewportSize({ width: 375, height: 780 });
  await expect(page.locator(".sandbox-shell > .mobile-bottom-nav")).toBeVisible();
  const mobileLayout = await page.locator(".sandbox-side-panel").evaluate((element) => ({
    columns: getComputedStyle(element).gridTemplateColumns,
    viewportWidth: window.innerWidth,
    right: element.getBoundingClientRect().right,
  }));
  expect(mobileLayout.columns.split(" ")).toHaveLength(1);
  expect(mobileLayout.right).toBeLessThanOrEqual(mobileLayout.viewportWidth);
});
