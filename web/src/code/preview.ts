// preview.ts — decides which workspace files open in the media preview
// surface instead of the Monaco text editor. The server enforces the same
// extension allowlist when streaming bytes; this mirror keeps the tree click
// path honest before any network round trip.

export type PreviewKind = "image" | "video" | "audio";

const IMAGE_EXTENSIONS = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg", "bmp", "ico", "avif"]);
const VIDEO_EXTENSIONS = new Set(["mp4", "m4v", "webm", "ogv"]);
const AUDIO_EXTENSIONS = new Set(["mp3", "wav", "ogg", "oga", "opus", "flac", "m4a", "aac", "weba"]);

export function previewKindForPath(path: string): PreviewKind | null {
  const extension = path.split(".").pop()?.toLowerCase() || "";
  if (IMAGE_EXTENSIONS.has(extension)) return "image";
  if (VIDEO_EXTENSIONS.has(extension)) return "video";
  if (AUDIO_EXTENSIONS.has(extension)) return "audio";
  return null;
}

export function isPreviewableName(name: string): boolean {
  return previewKindForPath(name) !== null;
}
