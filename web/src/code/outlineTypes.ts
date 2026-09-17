import type { IRange } from "monaco-editor";
import type { FileRef } from "./types";

export type OutlineTarget = {
  ref: FileRef;
  kind: "file" | "directory";
  isSymlink?: boolean;
  blockedReason?: string;
};

export type OutlineSymbol = {
  name: string;
  detail: string;
  kind: number;
  range: IRange;
  selectionRange: IRange;
  children: OutlineSymbol[];
};

export type OutlineResult = {
  status: "loading" | "ready" | "empty" | "unsupported" | "error" | "skipped";
  symbols: OutlineSymbol[];
  message?: string;
};

export type OutlineFile = OutlineResult & { ref: FileRef; label: string };
export type OutlineSnapshot = {
  grouped: boolean;
  loading: boolean;
  selectionEmpty: boolean;
  files: OutlineFile[];
  completed: number;
};
