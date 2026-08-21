import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

type ManifestIcon = {
  src: string;
  sizes: string;
  type: string;
  purpose: string;
};

type EchoManifest = {
  id: string;
  name: string;
  short_name: string;
  description: string;
  start_url: string;
  scope: string;
  display: string;
  background_color: string;
  theme_color: string;
  prefer_related_applications: boolean;
  icons: ManifestIcon[];
};

const sourceDirectory = dirname(fileURLToPath(import.meta.url));
const webDirectory = resolve(sourceDirectory, "..");
const publicDirectory = resolve(webDirectory, "public");

function readPNGDimensions(path: string): { width: number; height: number } {
  const png = readFileSync(path);
  expect(png.subarray(0, 8).toString("hex")).toBe("89504e470d0a1a0a");
  expect(png.subarray(12, 16).toString("ascii")).toBe("IHDR");
  return { width: png.readUInt32BE(16), height: png.readUInt32BE(20) };
}

describe("Echo PWA manifest", () => {
  it("declares a stable standalone application with installable PNG icons", () => {
    const manifest = JSON.parse(
      readFileSync(resolve(publicDirectory, "manifest.json"), "utf8"),
    ) as EchoManifest;

    expect(manifest).toMatchObject({
      id: "/",
      name: "Echo",
      short_name: "Echo",
      start_url: "/",
      scope: "/",
      display: "standalone",
      background_color: "#f7f3f1",
      theme_color: "#f7f3f1",
      prefer_related_applications: false,
    });

    expect(manifest.icons).toEqual([
      { src: "/icons/echo-48.png", sizes: "48x48", type: "image/png", purpose: "any" },
      { src: "/icons/echo-192.png", sizes: "192x192", type: "image/png", purpose: "any maskable" },
      { src: "/icons/echo-512.png", sizes: "512x512", type: "image/png", purpose: "any maskable" },
    ]);

    for (const icon of manifest.icons) {
      const [width, height] = icon.sizes.split("x").map(Number);
      expect(readPNGDimensions(resolve(publicDirectory, icon.src.slice(1)))).toEqual({ width, height });
    }
  });

  it("links the manifest, favicon, and both system theme colors", () => {
    const document = new DOMParser().parseFromString(
      readFileSync(resolve(webDirectory, "index.html"), "utf8"),
      "text/html",
    );

    expect(document.querySelector('link[rel="manifest"]')?.getAttribute("href")).toBe("/manifest.json");
    expect(document.querySelector('link[rel="icon"]')?.getAttribute("href")).toBe("/icons/echo-48.png");
    expect(document.querySelector('meta[name="theme-color"][media*="light"]')?.getAttribute("content")).toBe("#f7f3f1");
    expect(document.querySelector('meta[name="theme-color"][media*="dark"]')?.getAttribute("content")).toBe("#121214");
  });
});
