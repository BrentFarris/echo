import { describe, expect, it } from "vitest";
import { normalizePersistedSourceControlRepository, persistedSourceControlGroupId } from "./sourceControlSession";

const legacyRepository = {
  id: "old-git-id",
  label: "Project",
  parent: false,
  scopes: [{ rootId: "root", rootLabel: "Project", repoPrefix: "" }],
  revision: 3,
};

describe("Source Control session migration", () => {
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
  });
});
