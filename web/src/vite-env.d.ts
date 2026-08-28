/// <reference types="vite/client" />

declare module "monaco-editor/*";

declare module "@novnc/novnc" {
  export default class RFB extends EventTarget {
    constructor(target: HTMLElement, url: string, options?: { credentials?: { password?: string }; shared?: boolean; wsProtocols?: string[] });
    viewOnly: boolean;
    scaleViewport: boolean;
    clipViewport: boolean;
    resizeSession: boolean;
    qualityLevel: number;
    compressionLevel: number;
    disconnect(): void;
    focus(): void;
    clipboardPasteFrom(text: string): void;
    sendCredentials(credentials: { password?: string }): void;
  }
}
