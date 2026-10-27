export type LineEndingsOnSave = "unchanged" | "crlf" | "lf";

export function lineEndingsOnSave(settings: { editorLineEndingsOnSave?: unknown }): LineEndingsOnSave {
  const value = settings.editorLineEndingsOnSave;
  return value === "crlf" || value === "lf" ? value : "unchanged";
}
