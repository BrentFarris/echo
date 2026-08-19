import { describe, expect, it } from "vitest";
import {
  CHAT_ROUTE, NavigationTracker, codeFileRouteHash, codeOpenTargetFromHash, codeRouteHash,
  codeSidebarFromHash, routePathFromHash,
} from "./navigation";

describe("view route parsing", () => {
  it("routes a Source Control deep link through Echo Code", () => {
    expect(routePathFromHash("#/code?sidebar=git")).toBe("/code");
    expect(codeSidebarFromHash("#/code?sidebar=git")).toBe("git");
    expect(codeRouteHash("git")).toBe("#/code?sidebar=git");
  });

  it("defaults Code and unrelated routes to Explorer", () => {
    expect(codeSidebarFromHash("#/code")).toBe("explorer");
    expect(codeSidebarFromHash("#/code?sidebar=unknown")).toBe("explorer");
    expect(codeSidebarFromHash("#/home?sidebar=git")).toBe("explorer");
    expect(codeRouteHash("explorer")).toBe("#/code");
  });

  it("round-trips encoded file-open targets through the Code route", () => {
    const target = { rootId: "root:id", path: "docs/My File #1.md" };
    const hash = codeFileRouteHash(target);

    expect(routePathFromHash(hash)).toBe("/code");
    expect(codeSidebarFromHash(hash)).toBe("explorer");
    expect(codeOpenTargetFromHash(hash)).toEqual(target);
    expect(codeOpenTargetFromHash("#/home?rootId=root&path=main.go")).toBeNull();
  });
});

describe("NavigationTracker", () => {
  it("falls back to chat when Settings was loaded directly", () => {
    const navigation = new NavigationTracker();
    navigation.record("/settings");

    expect(navigation.hasSettingsOrigin()).toBe(false);
    expect(navigation.settingsReturnRoute()).toBe(CHAT_ROUTE);
  });

  it("returns to Code when Settings was opened from Code", () => {
    const navigation = new NavigationTracker();
    navigation.record("/code");
    navigation.record("/settings");

    expect(navigation.hasSettingsOrigin()).toBe(true);
    expect(navigation.settingsReturnRoute()).toBe("/code");
  });

  it("preserves either chat route when Settings is opened from chat", () => {
    for (const route of ["/", "/home"]) {
      const navigation = new NavigationTracker();
      navigation.record(route);
      navigation.record("/settings");

      expect(navigation.settingsReturnRoute()).toBe(route);
    }
  });

  it("does not replace the origin during repeated Settings renders", () => {
    const navigation = new NavigationTracker();
    navigation.record("/code");
    navigation.record("/settings");
    navigation.record("/settings");

    expect(navigation.settingsReturnRoute()).toBe("/code");
  });
});
