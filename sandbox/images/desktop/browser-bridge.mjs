import { createServer } from "node:http";
import { readFileSync } from "node:fs";
import { stat } from "node:fs/promises";
import { timingSafeEqual } from "node:crypto";
import path from "node:path";
import { chromium } from "playwright";

const profilePath = "/home/echo/.config/chromium";
const downloadPath = "/exchange/downloads";
const maximumBodyBytes = 1 << 20;
const maximumScreenshotBytes = 5 << 20;
const maximumElements = 400;

let context;
let activePage;
let startError = "";
let nextTabID = 1;
let nextNavigation = 1;
let references = new Map();
const pageIDs = new Map();
const navigationIDs = new Map();
let leaseToken = "";

function constantTimeEqual(left, right) {
  const a = Buffer.from(left);
  const b = Buffer.from(right);
  return a.length === b.length && timingSafeEqual(a, b);
}

async function authorized(request) {
  const provided = String(request.headers.authorization || "").replace(/^Bearer\s+/i, "");
  return leaseToken.length >= 32 && constantTimeEqual(provided, leaseToken);
}

function json(response, status, value) {
  const data = Buffer.from(JSON.stringify(value));
  response.writeHead(status, {
    "Content-Type": "application/json; charset=utf-8",
    "Content-Length": data.length,
    "Cache-Control": "no-store",
  });
  response.end(data);
}

async function bodyJSON(request) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > maximumBodyBytes) throw coded("request_too_large", "request body is too large");
    chunks.push(chunk);
  }
  if (!chunks.length) return {};
  try {
    return JSON.parse(Buffer.concat(chunks).toString("utf8"));
  } catch {
    throw coded("invalid_arguments", "request body must be valid JSON");
  }
}

function coded(code, message, status = 400) {
  const error = new Error(message);
  error.code = code;
  error.status = status;
  return error;
}

function absoluteWebURL(value) {
  let parsed;
  try { parsed = new URL(String(value || "").trim()); }
  catch { throw coded("invalid_url", "url must be absolute HTTP or HTTPS"); }
  if (!["http:", "https:"].includes(parsed.protocol)) throw coded("invalid_url", "url must use HTTP or HTTPS");
  return parsed.href;
}

function ensureActive(signal) {
  if (signal?.aborted) throw coded("browser_action_canceled", "browser action was canceled", 409);
}

function abortable(operation, signal, onAbort) {
  if (!signal) return operation;
  return new Promise((resolve, reject) => {
    let settled = false;
    const abort = () => {
      if (settled) return;
      settled = true;
      Promise.resolve(onAbort?.()).catch(() => {});
      reject(coded("browser_action_canceled", "browser action was canceled", 409));
    };
    signal.addEventListener("abort", abort, { once: true });
    Promise.resolve(operation).then((value) => {
      if (settled) return;
      settled = true;
      signal.removeEventListener("abort", abort);
      resolve(value);
    }, (error) => {
      if (settled) return;
      settled = true;
      signal.removeEventListener("abort", abort);
      reject(error);
    });
    if (signal.aborted) abort();
  });
}

async function cancellableLocatorAction(operation, signal, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  for (;;) {
    ensureActive(signal);
    const remaining = deadline - Date.now();
    if (remaining <= 0) return operation(1);
    try {
      return await operation(Math.min(remaining, 250));
    } catch (error) {
      ensureActive(signal);
      if (error?.name !== "TimeoutError" || Date.now() >= deadline) throw error;
    }
  }
}

function pageID(page) {
  if (!pageIDs.has(page)) pageIDs.set(page, `tab-${nextTabID++}`);
  return pageIDs.get(page);
}

function trackPage(page) {
  pageID(page);
  navigationIDs.set(page, nextNavigation++);
  activePage = page;
  page.on("framenavigated", (frame) => {
    if (frame === page.mainFrame()) {
      navigationIDs.set(page, nextNavigation++);
      references.clear();
    }
  });
  page.on("close", () => {
    pageIDs.delete(page);
    navigationIDs.delete(page);
    references.clear();
    if (activePage === page) activePage = context?.pages().find((candidate) => !candidate.isClosed());
  });
}

async function launch() {
  try {
    context = await chromium.launchPersistentContext(profilePath, {
      headless: false,
      chromiumSandbox: true,
      viewport: { width: 1440, height: 900 },
      screen: { width: 1440, height: 900 },
      acceptDownloads: true,
      downloadsPath: downloadPath,
      proxy: { server: "http://gateway:3128", bypass: "localhost,127.0.0.1,gateway,workbench,desktop" },
      locale: "en-US",
      args: ["--disable-features=Translate"],
    });
    await context.addInitScript(() => {
      const install = () => {
        if (window.__echoSandboxObserver) return;
        window.__echoSandboxDOMRevision = 1;
        const observer = new MutationObserver(() => { window.__echoSandboxDOMRevision += 1; });
        observer.observe(document, { subtree: true, childList: true, attributes: true, characterData: true });
        window.__echoSandboxObserver = observer;
      };
      if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", install, { once: true });
      else install();
    });
    for (const page of context.pages()) trackPage(page);
    context.on("page", trackPage);
    activePage ||= await context.newPage();
  } catch (error) {
    startError = String(error?.message || error);
  }
}

async function currentPage(params = {}) {
  if (!context) throw coded("browser_unavailable", startError || "browser is still starting", 503);
  if (params.tabId) {
    const selected = context.pages().find((page) => pageID(page) === params.tabId);
    if (!selected) throw coded("tab_not_found", "browser tab was not found", 404);
    activePage = selected;
  }
  if (!activePage || activePage.isClosed()) activePage = context.pages().find((page) => !page.isClosed());
  if (!activePage) activePage = await context.newPage();
  return activePage;
}

async function revision(page) {
  const dom = await page.evaluate(() => Number(window.__echoSandboxDOMRevision || 0)).catch(() => 0);
  return `${pageID(page)}:${navigationIDs.get(page) || 0}:${dom}`;
}

async function tabList() {
  if (!context) return [];
  return Promise.all(context.pages().filter((page) => !page.isClosed()).map(async (page) => ({
    id: pageID(page),
    active: page === activePage,
    url: page.url(),
    title: await page.title().catch(() => ""),
  })));
}

async function screenshot(page) {
  let data = await page.screenshot({ type: "png", fullPage: false, animations: "disabled" });
  let mediaType = "image/png";
  if (data.length > maximumScreenshotBytes) {
    data = await page.screenshot({ type: "jpeg", quality: 65, fullPage: false, animations: "disabled" });
    mediaType = "image/jpeg";
  }
  if (data.length > maximumScreenshotBytes) throw coded("screenshot_too_large", "browser screenshot exceeds the 5 MiB limit", 413);
  return { mediaType, bytes: data.length, dataBase64: data.toString("base64") };
}

async function describeElement(locator) {
  return locator.evaluate((element) => {
    const style = getComputedStyle(element);
    const rect = element.getBoundingClientRect();
    if (style.visibility === "hidden" || style.display === "none" || rect.width === 0 || rect.height === 0) return null;
    const role = element.getAttribute("role") || ({
      A: "link", BUTTON: "button", INPUT: "input", SELECT: "combobox", TEXTAREA: "textbox",
      SUMMARY: "button", IMG: "img",
    })[element.tagName] || element.tagName.toLowerCase();
    const name = element.getAttribute("aria-label") || element.getAttribute("title") || element.getAttribute("alt")
      || (element.labels?.[0]?.innerText) || element.innerText || element.getAttribute("placeholder") || element.getAttribute("name") || "";
    return {
      role,
      name: String(name).replace(/\s+/g, " ").trim().slice(0, 240),
      disabled: Boolean(element.disabled) || element.getAttribute("aria-disabled") === "true",
      checked: typeof element.checked === "boolean" ? element.checked : undefined,
      value: ["INPUT", "TEXTAREA", "SELECT"].includes(element.tagName) && element.type !== "password"
        ? String(element.value || "").slice(0, 240) : undefined,
    };
  }).catch(() => null);
}

async function browserSnapshot(params, signal) {
  ensureActive(signal);
  const page = await currentPage(params);
  await page.waitForLoadState("domcontentloaded", { timeout: 5000 }).catch(() => {});
  ensureActive(signal);
  const snapshotRevision = await revision(page);
  const nextReferences = new Map();
  const selector = "a,button,input,select,textarea,summary,[role],[contenteditable='true']";
  const candidates = page.locator(selector);
  const count = Math.min(await candidates.count(), maximumElements);
  const elements = [];
  for (let index = 0; index < count; index++) {
    ensureActive(signal);
    const locator = candidates.nth(index);
    const description = await describeElement(locator);
    if (!description) continue;
    const ref = `e${elements.length + 1}`;
    nextReferences.set(ref, { page, locator, revision: snapshotRevision });
    elements.push({ ref, ...description });
  }
  const accessibility = await page.locator("body").ariaSnapshot({ timeout: 5000 }).catch(() => "");
  const result = {
    tabId: pageID(page), url: page.url(), title: await page.title().catch(() => ""),
    revision: snapshotRevision, accessibility, elements, tabs: await tabList(),
  };
  if (params.screenshot) result.screenshot = await screenshot(page);
  ensureActive(signal);
  references = nextReferences;
  return result;
}

async function referenced(params) {
  const item = references.get(String(params.ref || ""));
  if (!item) throw coded("stale_element_reference", "element reference is missing or expired", 409);
  if (item.page.isClosed() || await revision(item.page) !== item.revision) {
    references.clear();
    throw coded("stale_element_reference", "page changed after the element reference was created", 409);
  }
  activePage = item.page;
  return item;
}

function safeUploadPath(value) {
  if (typeof value !== "string" || value.includes("\0")) throw coded("invalid_upload_path", "upload path is invalid");
  const normalized = path.posix.normalize(value);
  if (!(normalized.startsWith("/workspace/") || normalized === "/workspace" || normalized.startsWith("/exchange/") || normalized === "/exchange")) {
    throw coded("upload_path_blocked", "uploads must come from /workspace or /exchange", 403);
  }
  return normalized;
}

async function call(method, params = {}, signal) {
  ensureActive(signal);
  switch (method) {
    case "open": {
      const target = absoluteWebURL(params.url);
      const page = await currentPage(params);
      await page.goto(target, { waitUntil: params.waitUntil || "domcontentloaded", timeout: Math.min(Number(params.timeoutMs) || 30000, 120000) });
      ensureActive(signal);
      return { tabId: pageID(page), url: page.url(), title: await page.title() };
    }
    case "snapshot": return browserSnapshot(params, signal);
    case "click": {
      const item = await referenced(params);
      await cancellableLocatorAction((timeout) => item.locator.click({
        button: params.button || "left", clickCount: params.clickCount || 1, timeout, noWaitAfter: true,
      }), signal, Math.min(Number(params.timeoutMs) || 10000, 60000));
      ensureActive(signal);
      return { ok: true, revision: await revision(item.page) };
    }
    case "type": {
      const item = await referenced(params);
      const text = String(params.text ?? "");
      if (text.length > 32768) throw coded("text_too_large", "text is larger than 32 KiB");
      if (params.append) {
        await cancellableLocatorAction((timeout) => item.locator.focus({ timeout }), signal, Math.min(Number(params.timeoutMs) || 10000, 60000));
        const delay = Math.min(Number(params.delayMs) || 0, 1000);
        for (const character of text) {
          ensureActive(signal);
          await item.page.keyboard.insertText(character);
          if (delay) await abortable(item.page.waitForTimeout(delay), signal);
        }
      } else {
        await cancellableLocatorAction((timeout) => item.locator.fill(text, { timeout }), signal, Math.min(Number(params.timeoutMs) || 10000, 60000));
      }
      ensureActive(signal);
      if (params.submit) {
        await cancellableLocatorAction((timeout) => item.locator.press("Enter", { timeout, noWaitAfter: true }), signal, Math.min(Number(params.timeoutMs) || 10000, 60000));
      }
      ensureActive(signal);
      return { ok: true, revision: await revision(item.page) };
    }
    case "select": {
      const item = await referenced(params);
      const values = Array.isArray(params.values) ? params.values.map(String) : [String(params.value ?? "")];
      const selected = await cancellableLocatorAction((timeout) => item.locator.selectOption(values, { timeout }), signal, 10000);
      ensureActive(signal);
      return { ok: true, selected, revision: await revision(item.page) };
    }
    case "press": {
      const page = await currentPage(params);
      const key = String(params.key || "").trim();
      if (!key || key.length > 100) throw coded("invalid_key", "key is invalid");
      if (params.ref) {
        const item = await referenced(params);
        await cancellableLocatorAction((timeout) => item.locator.press(key, { timeout, noWaitAfter: true }), signal, 10000);
      }
      else await page.keyboard.press(key);
      ensureActive(signal);
      return { ok: true, revision: await revision(page) };
    }
    case "scroll": {
      const page = await currentPage(params);
      const deltaX = Math.max(-100000, Math.min(100000, Number(params.deltaX) || 0));
      const deltaY = Math.max(-100000, Math.min(100000, Number(params.deltaY) || 0));
      if (params.ref) await (await referenced(params)).locator.evaluate((element, delta) => element.scrollBy(delta.x, delta.y), { x: deltaX, y: deltaY });
      else await page.mouse.wheel(deltaX, deltaY);
      return { ok: true, revision: await revision(page) };
    }
    case "tabs": {
      if (params.action === "new") {
        activePage = await context.newPage();
        if (params.url) await activePage.goto(absoluteWebURL(params.url), { waitUntil: "domcontentloaded" });
      } else if (params.action === "close") {
        const page = await currentPage(params);
        await page.close();
      } else if (params.action === "select") {
        const page = await currentPage(params);
        await page.bringToFront();
      }
      ensureActive(signal);
      return { tabs: await tabList() };
    }
    case "wait": {
      const page = await currentPage(params);
      const timeout = Math.min(Number(params.timeoutMs) || 30000, 120000);
      if (params.text) await page.getByText(String(params.text), { exact: Boolean(params.exact) }).first().waitFor({ timeout });
      else if (params.url) await page.waitForURL(String(params.url), { timeout });
      else if (params.loadState) await page.waitForLoadState(params.loadState, { timeout });
      else await page.waitForTimeout(Math.min(Number(params.milliseconds) || 1000, 30000));
      return { ok: true, url: page.url(), title: await page.title() };
    }
    case "upload": {
      const item = await referenced(params);
      const paths = (Array.isArray(params.paths) ? params.paths : [params.path]).map(safeUploadPath);
      for (const filename of paths) {
        const info = await stat(filename).catch(() => null);
        if (!info?.isFile()) throw coded("upload_file_not_found", "upload file was not found", 404);
      }
      await cancellableLocatorAction((timeout) => item.locator.setInputFiles(paths, { timeout }), signal, 10000);
      ensureActive(signal);
      return { ok: true, files: paths.map((filename) => path.posix.basename(filename)), revision: await revision(item.page) };
    }
    default: throw coded("unknown_browser_action", "browser action is not supported", 404);
  }
}

const server = createServer(async (request, response) => {
  const controller = new AbortController();
  request.on("aborted", () => controller.abort());
  response.on("close", () => { if (!response.writableEnded) controller.abort(); });
  try {
    if (!await authorized(request)) return json(response, 401, { ok: false, code: "unauthorized", error: "unauthorized" });
    if (request.method === "GET" && request.url === "/v1/health") {
      return json(response, context ? 200 : 503, { ok: Boolean(context), protocolVersion: "1", error: startError || undefined });
    }
    if (request.method !== "POST" || request.url !== "/v1/call") return json(response, 404, { ok: false, code: "not_found", error: "not found" });
    const payload = await bodyJSON(request);
    const operation = call(String(payload.method || ""), payload.params || {}, controller.signal);
    const result = await abortable(operation, controller.signal, async () => {
      await activePage?.evaluate(() => window.stop()).catch(() => {});
    });
    return json(response, 200, { ok: true, data: result });
  } catch (error) {
    return json(response, Number(error.status) || 500, {
      ok: false,
      code: error.code || "browser_action_failed",
      error: error.code ? error.message : "browser action failed",
    });
  }
});

try {
  leaseToken = readFileSync(3, "utf8").trim();
  if (leaseToken.length < 32) throw new Error("lease token is invalid");
  server.listen(3000, "0.0.0.0");
  launch();
} catch (error) {
  console.error(`browser bridge startup failed: ${String(error?.message || error)}`);
  process.exit(1);
}

async function shutdown() {
  await context?.close().catch(() => {});
  server.close(() => process.exit(0));
}
process.on("SIGTERM", shutdown);
process.on("SIGINT", shutdown);
