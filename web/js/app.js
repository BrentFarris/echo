// app.js — SPA bootstrap: hash router, view mounting, and startup wiring.
//
// Views are plain ES modules that export { mount(el), unmount() }. The router
// maps hash routes (e.g. #/home) to view modules and swaps them into #app.

import * as ws from "./ws.js";
import { ensureAuthenticated } from "../src/auth/authGate.ts";
import { recordNavigationRoute, routePathFromHash } from "../src/navigation.ts";

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

function currentRoute() {
  return routePathFromHash(location.hash);
}

async function render() {
  const requestedRoute = currentRoute();
  const route = routes[requestedRoute] ? requestedRoute : "/";
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
    const view = await loader();
    currentView = view;
    view.mount(app);
  } catch (err) {
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
    ws.stop();
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
