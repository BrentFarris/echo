import { describe, expect, it } from "vitest";
import { randomUUID } from "./randomUUID";

describe("randomUUID", () => {
  it("uses the browser implementation when it is available", () => {
    expect(randomUUID({ randomUUID: () => "native-id" })).toBe("native-id");
  });

  it("creates an RFC 4122 version 4 UUID when randomUUID is unavailable", () => {
    const value = randomUUID({
      getRandomValues: (bytes) => {
        bytes.fill(0xab);
        return bytes;
      },
    });

    expect(value).toBe("abababab-abab-4bab-abab-abababababab");
  });
});
