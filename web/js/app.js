// app.js — SPA bootstrap: hash router, view mounting, and startup wiring.
//
// Views are plain ES modules that export { mount(el), unmount() }. The router
// maps hash routes (e.g. #/home) to view modules and swaps them into #app.

import * as ws from "./ws.js";

// Route table: hash path -> () => Promise<view module>.
// Views are lazy-loaded so the shell stays light.
const routes = {
  "/": () => import("./views/home.js"),
  "/home": () => import("./views/home.js"),
  "/settings": () => import("./views/settings.js"),
};

const app = document.getElementById("app");
let currentView = null;

function currentRoute() {
  const hash = location.hash.replace(/^#/, "");
  return hash || "/";
}

async function render() {
  const route = currentRoute();
  const loader = routes[route] || routes["/"];

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

// Start the real-time channel and render the initial view.
ws.start();
render();
