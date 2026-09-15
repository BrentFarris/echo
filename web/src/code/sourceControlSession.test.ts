import { describe, expect, it } from "vitest";
import { normalizePersistedSourceControlRepository, persistedSourceControlGroupId, persistedSourceControlPath } from "./sourceControlSession";

const legacyRepository = {
  id: "old-git-id",
  label: "Project",
  parent: false,
  scopes: [{ rootId: "root", rootLabel: "Project", repoPrefix: "" }],
  revision: 3,
};

describe("Source Control session migration", () => {
  it("recovers old absolute diff paths using their saved file scope", () => {
    const repository = normalizePersistedSourceControlRepository({ ...legacyRepository, scopes: [{ rootId: "root", rootLabel: "Project", repoPrefix: "subtree" }] });
    const ref = { rootId: "root", path: "src/main.go" };
    expect(persistedSourceControlPath(repository, "C:\\client\\src\\main.go", ref)).toBe("subtree/src/main.go");
    expect(persistedSourceControlPath(repository, "/client/src/main.go", ref)).toBe("subtree/src/main.go");
    expect(persistedSourceControlPath(repository, "subtree/src/main.go", ref)).toBe("subtree/src/main.go");
    expect(persistedSourceControlGroupId({ ...repository, providerId: "p4" }, "working", "123")).toBe("123");
  });
  it("assigns Git identity and capabilities to pre-v4 persisted repositories", () => {
    const migrated = normalizePersistedSourceControlRepository(legacyRepository);
    expect(migrated.providerId).toBe("git");
    expect(migrated.providerLabel).toBe("Git");
    expect(migrated.available).toBe(true);
    expect(migrated.capabilities).toContain("stage");
  });

  it("translates legacy Git diff scopes while preserving explicit groups", () => {
    const migrated = normalizePersistedSourceControlRepository(legacyRepository);
    expect(persistedSourceControlGroupId(migrated, "staged")).toBe("staged");
    expect(persistedSourceControlGroupId(migrated, "unstaged")).toBe("unstaged");
    expect(persistedSourceControlGroupId(migrated, "commit")).toBeUndefined();
    expect(persistedSourceControlGroupId(migrated, "unstaged", "working")).toBe("working");
    expect(persistedSourceControlGroupId(migrated, "included")).toBe("staged");
  });

  it("maps v5 semantic scopes for Fossil checkpoints", () => {
    const fossil = normalizePersistedSourceControlRepository({ ...legacyRepository, providerId: "fossil", providerLabel: "Fossil", capabilities: ["protect"] });
    expect(persistedSourceControlGroupId(fossil, "included")).toBe("protected");
    expect(persistedSourceControlGroupId(fossil, "working")).toBe("working");
  });
});
