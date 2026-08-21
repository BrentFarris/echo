import { afterEach, describe, expect, it, vi } from "vitest";
import {
  mountSpeechRecognition, type SpeechRecognitionLike,
} from "./speechRecognition";

class FakeRecognition implements SpeechRecognitionLike {
  continuous = false;
  interimResults = false;
  lang = "";
  maxAlternatives = 0;
  onstart: SpeechRecognitionLike["onstart"] = null;
  onresult: SpeechRecognitionLike["onresult"] = null;
  onerror: SpeechRecognitionLike["onerror"] = null;
  onend: SpeechRecognitionLike["onend"] = null;
  start = vi.fn();
  stop = vi.fn();
  abort = vi.fn();
}

function recognitionResult(transcript: string, isFinal: boolean) {
  return { 0: { transcript }, isFinal };
}

function resultEvent(resultIndex: number, results: ReturnType<typeof recognitionResult>[]) {
  return { resultIndex, results: Object.assign(results, { length: results.length }) } as unknown as Parameters<NonNullable<SpeechRecognitionLike["onresult"]>>[0];
}

function errorEvent(error: string) {
  return { error } as unknown as Parameters<NonNullable<SpeechRecognitionLike["onerror"]>>[0];
}

function setup(initialText = "") {
  const button = document.createElement("button");
  const input = document.createElement("div");
  input.contentEditable = "true";
  input.textContent = initialText;
  document.body.append(input, button);
  const range = document.createRange();
  range.selectNodeContents(input);
  range.collapse(false);
  window.getSelection()?.removeAllRanges();
  window.getSelection()?.addRange(range);
  const recognition = new FakeRecognition();
  const onError = vi.fn();
  const controller = mountSpeechRecognition({
    button, input, onError, createRecognition: () => recognition,
  });
  return { button, input, recognition, onError, controller };
}

afterEach(() => {
  document.body.replaceChildren();
  vi.restoreAllMocks();
});

describe("prompt speech recognition", () => {
  it("inserts interim and final speech at the caret without submitting", () => {
    const { button, input, recognition, controller } = setup("Review this");
    const inputEvent = vi.fn();
    input.addEventListener("input", inputEvent);

    button.click();
    expect(recognition.start).toHaveBeenCalledOnce();
    expect(button.getAttribute("aria-pressed")).toBe("true");
    recognition.onstart?.(new Event("start"));
    expect(button.classList.contains("is-listening")).toBe(true);

    recognition.onresult?.(resultEvent(0, [recognitionResult("use the microphone", false)]));
    expect(input.textContent).toBe("Review this use the microphone");
    expect(input.querySelector(".is-interim")?.textContent).toBe("use the microphone");

    recognition.onresult?.(resultEvent(0, [recognitionResult("use the microphone", true)]));
    expect(input.textContent).toBe("Review this use the microphone");
    expect(input.querySelector(".is-interim")).toBeNull();
    expect(inputEvent).toHaveBeenCalled();

    button.click();
    expect(recognition.stop).toHaveBeenCalledOnce();
    recognition.onend?.(new Event("end"));
    expect(button.getAttribute("aria-pressed")).toBe("false");
    expect(input.querySelector("[data-chat-voice-transcript]")).toBeNull();
    expect(input.textContent).toBe("Review this use the microphone");
    controller.dispose();
  });

  it("keeps finalized phrases when recognition returns more results", () => {
    const { button, input, recognition } = setup();
    button.click();
    recognition.onstart?.(new Event("start"));
    recognition.onresult?.(resultEvent(0, [recognitionResult("create a", true)]));
    recognition.onresult?.(resultEvent(1, [
      recognitionResult("create a", true), recognitionResult("new test", true),
    ]));
    recognition.onend?.(new Event("end"));
    expect(input.textContent).toBe("create a new test");
  });

  it("inserts speech at the selected caret position with readable spacing", () => {
    const { button, input, recognition } = setup("Review now");
    const range = document.createRange();
    range.setStart(input.firstChild!, 6);
    range.collapse(true);
    window.getSelection()?.removeAllRanges();
    window.getSelection()?.addRange(range);

    button.click();
    recognition.onstart?.(new Event("start"));
    recognition.onresult?.(resultEvent(0, [recognitionResult("this", true)]));
    recognition.onend?.(new Event("end"));

    expect(input.textContent).toBe("Review this now");
  });

  it("reports permission errors and leaves the draft intact", () => {
    const { button, input, recognition, onError } = setup("Keep this draft");
    button.click();
    recognition.onerror?.(errorEvent("not-allowed"));
    recognition.onend?.(new Event("end"));
    expect(onError).toHaveBeenCalledWith(
      "Microphone access was denied. Allow microphone access for Echo, then try again.",
    );
    expect(input.textContent).toBe("Keep this draft");
  });

  it("disables the microphone when browser recognition is unavailable", () => {
    const button = document.createElement("button");
    const input = document.createElement("div");
    const speechWindow = window as Window & { SpeechRecognition?: unknown; webkitSpeechRecognition?: unknown };
    delete speechWindow.SpeechRecognition;
    delete speechWindow.webkitSpeechRecognition;

    mountSpeechRecognition({ button, input });

    expect(button.disabled).toBe(true);
    expect(button.title).toBe("Voice input is not supported by this browser");
    expect(button.getAttribute("aria-label")).toBe("Voice input unavailable");
  });
});
