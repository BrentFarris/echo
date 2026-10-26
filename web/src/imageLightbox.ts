// Full-screen image lightbox shared by every chat surface. Any <img> that
// represents a user attachment or AI-read/AI-generated image can call
// openImageLightbox(src, { name, alt }) to show it full screen. Closes via the
// top-right X button, the Esc key, or a click on the backdrop.

import { icons } from "../js/icons.js";

type LightboxOptions = {
  name?: string;
  alt?: string;
  // fullSrc is the full-size source to open in the lightbox when the inline
  // image is a downscaled preview variant. Falls back to the image's own src.
  fullSrc?: string;
};

let active: {
  root: HTMLElement;
  keydown: (event: KeyboardEvent) => void;
  restoreFocus: Element | null;
} | null = null;

export function openImageLightbox(src: string, options: LightboxOptions = {}): void {
  if (!src) return;
  // A lightbox is exclusive: opening a new one replaces the current one.
  closeImageLightbox();

  const name = options.name || "Image";
  const root = document.createElement("div");
  root.className = "image-lightbox";
  root.setAttribute("role", "dialog");
  root.setAttribute("aria-modal", "true");
  root.setAttribute("aria-label", `Full screen image preview: ${name}`);

  const image = document.createElement("img");
  image.className = "image-lightbox-image";
  image.src = src;
  image.alt = options.alt || name;

  const close = document.createElement("button");
  close.type = "button";
  close.className = "image-lightbox-close icon-button";
  close.title = "Close (Esc)";
  close.setAttribute("aria-label", "Close image preview");
  close.innerHTML = icons.x;

  const caption = document.createElement("div");
  caption.className = "image-lightbox-caption";
  caption.textContent = name;

  root.append(image, close, caption);
  // Backdrop clicks close; clicks on the image itself do not (so users can
  // still drag-select or copy it).
  root.addEventListener("click", (event) => {
    if (event.target === root) closeImageLightbox();
  });
  close.addEventListener("click", () => closeImageLightbox());

  const keydown = (event: KeyboardEvent) => {
    if (event.key !== "Escape") return;
    event.preventDefault();
    event.stopPropagation();
    closeImageLightbox();
  };
  document.addEventListener("keydown", keydown, true);

  const restoreFocus = document.activeElement;
  document.body.appendChild(root);
  document.body.classList.add("image-lightbox-open");
  close.focus();

  active = { root, keydown, restoreFocus };
}

export function closeImageLightbox(): void {
  if (!active) return;
  const { root, keydown, restoreFocus } = active;
  active = null;
  document.removeEventListener("keydown", keydown, true);
  root.remove();
  document.body.classList.remove("image-lightbox-open");
  if (restoreFocus instanceof HTMLElement) restoreFocus.focus();
}

// makeImageZoomable wires the standard click-to-zoom behavior onto an <img>.
// Used by chat media figures and composer attachment drafts so every image
// behaves the same.
export function makeImageZoomable(image: HTMLImageElement, options: LightboxOptions = {}): void {
  image.classList.add("image-zoomable");
  image.title = "Click to view full screen";
  image.addEventListener("click", () => openImageLightbox(options.fullSrc || image.src, {
    name: options.name || image.alt,
    alt: options.alt || image.alt,
  }));
}
