// Shared volume pill for <video> elements: a compact mute toggle + slider
// rendered inside the player surface. Native <video controls> ship no usable
// volume UI on most mobile browsers, so this control provides consistent
// volume access on every platform. State stays synchronized with the element
// (dragging the slider un-mutes; changes made via the native controls are
// reflected back here).

import { icons } from "../js/icons.js";

let lastVolume = 1;

export function attachVideoVolumeControl(video: HTMLVideoElement, className: string): HTMLElement {
  const pill = document.createElement("div");
  pill.className = className;
  const button = document.createElement("button");
  button.type = "button";
  button.className = `${className}-toggle`;
  const slider = document.createElement("input");
  slider.type = "range";
  slider.min = "0";
  slider.max = "1";
  slider.step = "0.05";
  slider.value = String(lastVolume);
  slider.setAttribute("aria-label", "Volume");
  const updateIcon = (): void => {
    const silent = video.muted || video.volume === 0;
    button.innerHTML = silent ? icons.volumeMute : icons.volumeHigh;
    button.title = silent ? "Unmute" : "Mute";
    button.setAttribute("aria-pressed", String(!silent));
  };
  slider.addEventListener("input", () => {
    lastVolume = Number(slider.value);
    video.volume = lastVolume;
    video.muted = lastVolume === 0;
    updateIcon();
  });
  button.addEventListener("click", () => {
    video.muted = !video.muted;
    updateIcon();
  });
  video.addEventListener("volumechange", () => {
    slider.value = String(video.volume);
    lastVolume = video.volume;
    updateIcon();
  });
  pill.append(button, slider);
  updateIcon();
  return pill;
}
