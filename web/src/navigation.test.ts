import { describe, expect, it } from "vitest";
import { CHAT_ROUTE, NavigationTracker } from "./navigation";

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
