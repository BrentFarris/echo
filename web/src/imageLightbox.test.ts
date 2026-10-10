import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { closeImageLightbox, makeImageZoomable, openImageLightbox } from "./imageLightbox";

const src = "data:image/png;base64,YQ==";

describe("image lightbox", () => {
  beforeEach(() => {
    document.body.innerHTML = "";
  });

  afterEach(() => {
    closeImageLightbox();
    document.body.innerHTML = "";
  });

  it("opens a full-screen overlay with the image, close button, and caption", () => {
    openImageLightbox(src, { name: "diagram.png" });

    const root = document.querySelector(".image-lightbox");
    expect(root).not.toBeNull();
    expect(root!.getAttribute("role")).toBe("dialog");
    expect(document.querySelector<HTMLImageElement>(".image-lightbox-image")!.src).toBe(src);
    expect(document.querySelector(".image-lightbox-caption")!.textContent).toBe("diagram.png");
    expect(document.body.classList.contains("image-lightbox-open")).toBe(true);
  });

  it("closes via the X button and via Esc", () => {
    openImageLightbox(src, { name: "diagram.png" });

    document.querySelector<HTMLButtonElement>(".image-lightbox-close")!.click();
    expect(document.querySelector(".image-lightbox")).toBeNull();
    expect(document.body.classList.contains("image-lightbox-open")).toBe(false);

    openImageLightbox(src);
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    expect(document.querySelector(".image-lightbox")).toBeNull();
  });

  it("closes on backdrop clicks but not image clicks", () => {
    openImageLightbox(src);

    document.querySelector(".image-lightbox-image")!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(document.querySelector(".image-lightbox")).not.toBeNull();

    document.querySelector(".image-lightbox")!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    expect(document.querySelector(".image-lightbox")).toBeNull();
  });

  it("replaces an existing lightbox when a new image opens", () => {
    openImageLightbox(src, { name: "a.png" });
    openImageLightbox(src, { name: "b.png" });
    expect(document.querySelectorAll(".image-lightbox").length).toBe(1);
    expect(document.querySelector(".image-lightbox-caption")!.textContent).toBe("b.png");
  });

  it("makeImageZoomable opens the lightbox on click", () => {
    const image = document.createElement("img");
    image.src = src;
    image.alt = "pic.png";
    document.body.append(image);
    makeImageZoomable(image);

    expect(image.classList.contains("image-zoomable")).toBe(true);
    image.click();
    expect(document.querySelector(".image-lightbox")).not.toBeNull();
    expect(document.querySelector(".image-lightbox-caption")!.textContent).toBe("pic.png");
  });
});
