import { describe, expect, it } from "vitest";
import { renderPrimaryNav } from "./primaryNav";

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
  });

  it("marks the selected Code sidebar item active", () => {
    const git = render({ active: "git", workspaceName: "Example" });

    expect(git.querySelector("[data-code-sidebar=git]")?.classList.contains("is-active")).toBe(true);
    expect(git.querySelector("[data-code-sidebar=explorer]")?.classList.contains("is-active")).toBe(false);
  });

  it("escapes workspace names", () => {
    const nav = render({ active: "explorer", workspaceName: '<script>alert("no")</script>' });

    expect(nav.querySelector("script")).toBeNull();
    expect(nav.querySelector("[data-nav=workspace]")?.getAttribute("title"))
      .toBe('Workspace: <script>alert("no")</script>');
  });
});
