import { expect, test } from "@playwright/test";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const directory = dirname(fileURLToPath(import.meta.url));

test("F11 and Step Into cross CGO, inspect C, and return to Go", async ({ page }) => {
  test.setTimeout(90_000);
  const state = JSON.parse(readFileSync(resolve(directory, "../test-results/e2e-runtime/state.json"), "utf8"));
  // Native code deliberately lives outside the workspace and opens read-only.
  const nativeSource = join(dirname(state.workspace), "native.c");
  writeFileSync(nativeSource, [...Array.from({ length: 21 }, (_, i) => `// native source ${i + 1}`),
    "int native_add(int value) {", "    int x = 42;", "    x += ptr->items[1];", "    return x;", "}", "",
  ].join("\n"));

  await page.goto("/");
  if (await page.getByRole("heading", { name: "Secure this Echo server" }).isVisible()) {
    await page.getByLabel("Setup code").fill(state.setupCode);
    await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
    await page.getByLabel("Confirm password").fill("Echo-E2E-Password!");
    await page.getByLabel("Device name").fill("Playwright CGO");
    await page.getByRole("button", { name: "Finish setup" }).click();
  } else {
    await page.getByLabel("Password", { exact: true }).fill("Echo-E2E-Password!");
    await page.getByLabel("Device name").fill("Playwright CGO");
    await page.getByRole("button", { name: "Sign in" }).click();
  }
  await expect(page.locator(".app-shell")).toBeVisible();
  await page.evaluate(async ({ state, script, nativeSource }) => {
    async function request(path: string, method = "GET", body?: unknown) {
      const response = await fetch(path, { method, headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
      const payload = await response.json();
      if (!response.ok || payload.ok === false) throw new Error(JSON.stringify(payload));
      return payload.data;
    }
    const workspaces = (await request("/api/workspaces")).workspaces;
    const workspace = workspaces.find((w: { mainPath: string }) => w.mainPath === state.workspace)
      || (await request("/api/workspaces", "POST", { name: "CGO E2E", mainPath: state.workspace, folders: [] })).workspace;
    await request("/api/workspaces/active", "PUT", { id: workspace.id });
    await request("/api/debug/adapter-profiles", "POST", { profile: {
      id: "cgo-e2e", name: "CGO E2E", adapterId: "go", command: state.nodePath, args: [script],
      selectors: [{ languageId: "go", extensions: [".go"] }], transport: { kind: "stdio", startupTimeoutMs: 15000 },
    } });
    await request(`/api/workspaces/${workspace.id}/debug/config`, "PUT", {
      version: 1, enabledAdapterProfileIds: ["cgo-e2e"], configurations: [{
        id: "cgo", name: "CGO", adapterProfileId: "cgo-e2e", request: "launch",
        arguments: { program: "${workspaceFolder}/main.go", testCGO: true, nativeSource },
      }],
    });
  }, { state, script: resolve(directory, "fake-dap.mjs"), nativeSource });
  await page.goto("/#/code");
  await page.locator(".code-tree-label", { hasText: "main.go" }).click();
  await page.locator(".view-line", { hasText: "Target()" }).click();
  await page.keyboard.press("Control+5");
  await page.keyboard.press("F5");
  await expect(page.locator(".debug-variable", { hasText: "x" }).first()).toContainText("42");

  await page.locator(".view-lines").click();
  await page.keyboard.press("F11");
  await expect(page.locator(".code-tab.is-active")).toContainText("native.c");
  await expect(page.getByRole("button", { name: /^C\.native_add native\.c:23/ })).toBeVisible();
  await expect(page.locator(".echo-debug-current-line")).toBeVisible();
  await expect(page.locator(".debug-variable", { hasText: "x" }).first()).toContainText("42");
  await expect(page.getByRole("button", { name: /^main\.main main\.go:4/ })).toBeVisible();

  await page.getByRole("button", { name: "Add Watch Expression" }).click();
  const expression = page.getByRole("textbox", { name: "Expression", exact: true });
  await expression.fill("ptr->items[1]");
  await expression.press("Enter");
  await expect(page.locator(".debug-watch-row", { hasText: "ptr->items[1]" })).toContainText("2");
  const pointer = await page.locator(".view-line", { hasText: "ptr->items[1]" }).evaluate((line) => {
    const nodes = document.createTreeWalker(line, NodeFilter.SHOW_TEXT);
    for (let node = nodes.nextNode(); node; node = nodes.nextNode()) {
      const offset = node.textContent?.indexOf("items") ?? -1;
      if (offset < 0) continue;
      const range = document.createRange();
      range.setStart(node, offset); range.setEnd(node, offset + 1);
      const rect = range.getBoundingClientRect();
      return { x: rect.x + rect.width / 2, y: rect.y + rect.height / 2 };
    }
    throw new Error("Native pointer expression was not rendered");
  });
  await page.mouse.move(pointer.x, pointer.y);
  await expect(page.locator(".monaco-hover", { hasText: "ptr->items[1]" })).toContainText("2");

  await page.locator(".debug-floating-toolbar [data-debug-control=stepOut]").click();
  await expect(page.locator(".code-tab.is-active")).toContainText("main.go");
  await page.locator(".debug-floating-toolbar [data-debug-control=stepIn]").click();
  await expect(page.getByRole("button", { name: /^C\.native_add native\.c:23/ })).toBeVisible();
  await expect(page.locator(".code-tab.is-active")).toContainText("native.c");
  await expect(page.locator(".code-tab", { hasText: "native.c" })).toHaveCount(1);
  await page.locator(".debug-floating-toolbar [data-debug-control=next]").click();
  await expect(page.getByRole("button", { name: /^C\.native_add native\.c:24/ })).toBeVisible();
  await page.reload();
  await expect(page.locator(".code-tab.is-active")).toContainText("native.c");
  await expect(page.getByRole("button", { name: /^C\.native_add native\.c:24/ })).toBeVisible();
  await page.locator(".debug-floating-toolbar [data-debug-action=stop]").click();
  await expect(page.locator(".debug-floating-toolbar")).toHaveCount(0);
});
