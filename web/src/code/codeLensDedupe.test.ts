import { describe, expect, it } from "vitest";
import { beginGoTestLensComposition, builtInGoTestLensKeys, codeLensIdentity } from "./codeLensDedupe";

function model(uri: string, version = 1): any {
  return { uri: { toString: () => uri }, getVersionId: () => version };
}

describe("CodeLens composition", () => {
  it("lets the LSP side wait for same-document built-in identities", async () => {
    const document = model("file:///workspace/sample_test.go");
    const pending = builtInGoTestLensKeys(document);
    const complete = beginGoTestLensComposition(document);
    const lens = {
      range: { start: { line: 4, character: 0 }, end: { line: 4, character: 0 } },
      title: "run test",
    };
    complete([lens]);

    expect(await pending).toContain(codeLensIdentity(lens));
  });

  it("does not delay LSP-only documents", async () => {
    expect(await builtInGoTestLensKeys(model("file:///workspace/main.go"))).toEqual(new Set());
  });
});
