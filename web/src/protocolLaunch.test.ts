import { describe, expect, it } from "vitest";
import { resolveProtocolLaunch } from "./protocolLaunch.js";

describe("resolveProtocolLaunch", () => {
  it("leaves ordinary URLs untouched", () => {
    const result = resolveProtocolLaunch("", "3740");
    expect(result).toEqual({ redirect: null, cleanedSearch: "" });

    const withQuery = resolveProtocolLaunch("?workspace=abc", "3740");
    expect(withQuery).toEqual({ redirect: null, cleanedSearch: "?workspace=abc" });
  });

  it("strips the echo parameter for a same-port launch", () => {
    const result = resolveProtocolLaunch("?echo=echo%3A%2F%2Fopen%3Fport%3D3740", "3740");
    expect(result).toEqual({ redirect: null, cleanedSearch: "" });
  });

  it("keeps other parameters while stripping echo", () => {
    const result = resolveProtocolLaunch("?echo=echo%3A%2F%2Fopen%3Fport%3D3740&workspace=abc", "3740");
    expect(result).toEqual({ redirect: null, cleanedSearch: "workspace=abc" });
  });

  it("redirects when the deep link targets a different port", () => {
    const result = resolveProtocolLaunch("?echo=echo%3A%2F%2Fopen%3Fport%3D3741", "3740");
    expect(result).toEqual({ redirect: "http://localhost:3741/", cleanedSearch: "" });
  });

  it("ignores malformed echo values", () => {
    const result = resolveProtocolLaunch("?echo=not-a-url", "3740");
    expect(result).toEqual({ redirect: null, cleanedSearch: "" });
  });

  it("ignores non-numeric ports", () => {
    const result = resolveProtocolLaunch("?echo=echo%3A%2F%2Fopen%3Fport%3Dabc", "3740");
    expect(result).toEqual({ redirect: null, cleanedSearch: "" });
  });
});
