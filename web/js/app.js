// app.js — SPA bootstrap: hash router, view mounting, and startup wiring.
//
// Views are plain ES modules that export { mount(el), unmount() }. The router
// maps hash routes (e.g. #/home) to view modules and swaps them into #app.

import * as ws from "./ws.js";
import { ensureAuthenticated } from "../src/auth/authGate.ts";
import { recordNavigationRoute, routePathFromHash } from "../src/navigation.ts";
import { initializePluginHost, mountPluginPage, resetPluginHost } from "../src/plugins/pluginHost.ts";

// Route table: hash path -> () => Promise<view module>.
// Views are lazy-loaded so the shell stays light.
const routes = {
  "/": () => import("./views/home.js"),
  "/home": () => import("./views/home.js"),
  "/settings": () => import("./views/settings.js"),
  "/code": () => import("../src/code/codeView.ts"),
};

const app = document.getElementById("app");
let currentView = null;
let renderGeneration = 0;

function currentRoute() {
  return routePathFromHash(location.hash);
}

async function render() {
  const generation = ++renderGeneration;
  const requestedRoute = currentRoute();
  const pluginMatch = requestedRoute.match(/^\/plugins\/([^/]+)\/([^/]+)$/);
  const route = routes[requestedRoute] ? requestedRoute : pluginMatch ? requestedRoute : "/";
  const loader = routes[route];
  recordNavigationRoute(route);

  // Tear down the previous view.
  if (currentView?.unmount) {
    try {
      currentView.unmount();
    } catch (err) {
      console.error("view unmount error:", err);
    }
  }
  currentView = null;
  app.innerHTML = "";

  try {
    if (pluginMatch) {
      const mounted = await mountPluginPage(app, decodeURIComponent(pluginMatch[1]), decodeURIComponent(pluginMatch[2]));
      if (generation !== renderGeneration) {
        mounted?.unmount?.();
        return;
      }
      currentView = mounted;
      return;
    }
    const view = await loader();
    if (generation !== renderGeneration) return;
    currentView = view;
    view.mount(app);
  } catch (err) {
    if (generation !== renderGeneration) return;
    console.error("failed to load view:", err);
    app.innerHTML = `<div class="card"><h2>Failed to load view</h2><p>${escapeHtml(String(err))}</p></div>`;
  }
}

function escapeHtml(value) {
  return value.replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[ch]));
}

window.addEventListener("hashchange", render);

let authenticating = false;
async function bootstrap() {
  if (authenticating) return;
  authenticating = true;
  try {
    ++renderGeneration;
    ws.stop();
    resetPluginHost();
    if (currentView?.unmount) {
      try {
        currentView.unmount();
      } catch (err) {
        console.error("view unmount error:", err);
      }
    }
    currentView = null;
    app.innerHTML = "";
    await ensureAuthenticated(app);
    ws.start();
    await initializePluginHost();
    await render();
  } catch (err) {
    console.error("authentication bootstrap failed:", err);
    app.innerHTML = `<main class="auth-screen"><section class="auth-panel"><h1>Echo is unavailable</h1><p>${escapeHtml(String(err))}</p><button class="auth-submit" data-auth-retry>Retry</button></section></main>`;
    app.querySelector("[data-auth-retry]")?.addEventListener("click", () => location.reload());
  } finally {
    authenticating = false;
  }
}

window.addEventListener("echo:unauthorized", bootstrap);
window.addEventListener("echo:logged-out", bootstrap);
bootstrap();
