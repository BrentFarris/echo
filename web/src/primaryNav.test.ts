import { describe, expect, it } from "vitest";
import { renderMobilePrimaryNav, renderPrimaryNav } from "./primaryNav";

function render(options: Parameters<typeof renderPrimaryNav>[0]): HTMLElement {
  const host = document.createElement("div");
  host.innerHTML = renderPrimaryNav(options);
  return host;
}

describe("primary navigation", () => {
  it("uses the same Code activity bar structure for Chat", () => {
    const chat = render({ active: "chat", workspaceName: "Echo", workspaceSelector: true });
    const code = render({ active: "explorer", workspaceName: "Echo" });

    expect(chat.querySelector(".code-left-nav")).not.toBeNull();
    expect([...chat.querySelectorAll("[data-nav]")].map((item) => item.getAttribute("data-nav")))
      .toEqual([...code.querySelectorAll("[data-nav]")].map((item) => item.getAttribute("data-nav")));
    expect(chat.querySelectorAll(".nav-icon-button")).toHaveLength(code.querySelectorAll(".nav-icon-button").length);
    expect(chat.querySelector("[data-nav=chat]")?.classList.contains("is-active")).toBe(true);
    expect(chat.querySelector("[data-nav=code]")?.classList.contains("is-active")).toBe(false);
    expect(chat.querySelector("[aria-label=Tasks]")).toBeNull();
    expect(chat.querySelector("[data-nav=settings] [data-echo-update-badge]")).not.toBeNull();
    expect([...chat.querySelectorAll(".left-nav-buttons [data-nav]")].map((item) => item.getAttribute("data-nav")))
      .toEqual(["chat", "code", "search", "git"]);
    expect([...chat.querySelectorAll(".left-nav-actions [data-nav]")].map((item) => item.getAttribute("data-nav")))
      .toEqual(["map", "settings"]);
    expect(chat.querySelector("[data-nav=map]")?.getAttribute("aria-expanded")).toBe("false");
  });

  it("marks the selected Code sidebar item active", () => {
    const git = render({ active: "git", workspaceName: "Example" });

    expect(git.querySelector("[data-code-sidebar=git]")?.classList.contains("is-active")).toBe(true);
    expect(git.querySelector("[data-code-sidebar=explorer]")?.classList.contains("is-active")).toBe(false);
    expect(git.querySelector("[data-code-sidebar=search]")?.classList.contains("is-active")).toBe(false);
  });

  it("renders a custom workspace icon on a workspace selector", () => {
    const nav = render({
      active: "explorer",
      workspaceName: "Example",
      workspaceSelector: true,
      workspaceIconUrl: "/api/workspaces/example/icon",
    });

    const selector = nav.querySelector("[data-nav=workspace]");
    expect(selector?.classList.contains("workspace-dropdown-trigger")).toBe(true);
    expect(selector?.getAttribute("aria-expanded")).toBe("false");
    expect(selector?.querySelector("img")?.getAttribute("src")).toBe("/api/workspaces/example/icon");
    expect(selector?.querySelector(".workspace-icon-label")?.textContent).toBe("");
  });

  it("escapes workspace names", () => {
    const nav = render({ active: "explorer", workspaceName: '<script>alert("no")</script>' });

    expect(nav.querySelector("script")).toBeNull();
    expect(nav.querySelector("[data-nav=workspace]")?.getAttribute("title"))
      .toBe('Workspace: <script>alert("no")</script>');
  });

  it("renders the enabled mobile destinations with the selected view marked current", () => {
    const mobile = renderMobilePrimaryNav({ active: "git", workspaceName: "Echo", workspaceSelector: true });
    const host = document.createElement("div");
    host.innerHTML = mobile;

    expect(host.querySelector("[data-mobile-primary-nav]")).not.toBeNull();
    expect([...host.querySelectorAll(".mobile-nav-tab")].map((item) => item.getAttribute("aria-label")))
      .toEqual(["Chat", "Code", "Search", "Source Control", "Chat Map", "Settings"]);
    expect(host.querySelector("[aria-label=Tasks]")).toBeNull();
    expect(host.querySelector("[data-nav=git]")?.getAttribute("aria-current")).toBe("page");
    expect(host.querySelector("[data-nav=code]")?.hasAttribute("aria-current")).toBe(false);
    expect(host.querySelector("[data-git-badge]")).not.toBeNull();
    expect(host.querySelector("[data-nav=settings] [data-echo-update-badge]")).not.toBeNull();
    expect(host.querySelector(".mobile-nav-global-actions [data-nav=map]")?.nextElementSibling?.getAttribute("data-nav"))
      .toBe("settings");
    expect(host.querySelector(".workspace-dropdown-trigger")?.getAttribute("aria-expanded")).toBe("false");
  });

  it("escapes mobile workspace names and marks Settings active", () => {
    const host = document.createElement("div");
    host.innerHTML = renderMobilePrimaryNav({
      active: "settings",
      workspaceName: '<img src=x onerror="alert(1)">',
      workspaceSelector: true,
    });

    expect(host.querySelector("img")).toBeNull();
    expect(host.querySelector("[data-mobile-workspace-name]")?.textContent)
      .toBe('<img src=x onerror="alert(1)">');
    expect(host.querySelector("[data-nav=settings]")?.classList.contains("is-active")).toBe(true);
    expect(host.querySelector("[data-nav=settings]")?.getAttribute("aria-current")).toBe("page");
  });
});
