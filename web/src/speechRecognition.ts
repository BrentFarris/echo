export type SpeechRecognitionError =
  | "aborted"
  | "audio-capture"
  | "bad-grammar"
  | "language-not-supported"
  | "network"
  | "no-speech"
  | "not-allowed"
  | "service-not-allowed"
  | string;

type SpeechRecognitionAlternativeLike = { transcript: string };
type SpeechRecognitionResultLike = {
  readonly isFinal: boolean;
  readonly 0?: SpeechRecognitionAlternativeLike;
};
type SpeechRecognitionResultListLike = {
  readonly length: number;
  readonly [index: number]: SpeechRecognitionResultLike;
};

export type SpeechRecognitionLike = {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  maxAlternatives: number;
  onstart: ((event: Event) => void) | null;
  onresult: ((event: Event & { resultIndex: number; results: SpeechRecognitionResultListLike }) => void) | null;
  onerror: ((event: Event & { error: SpeechRecognitionError }) => void) | null;
  onend: ((event: Event) => void) | null;
  start(): void;
  stop(): void;
  abort(): void;
};

type SpeechRecognitionConstructor = new () => SpeechRecognitionLike;

type SpeechRecognitionWindow = Window & typeof globalThis & {
  SpeechRecognition?: SpeechRecognitionConstructor;
  webkitSpeechRecognition?: SpeechRecognitionConstructor;
};

export type SpeechRecognitionController = {
  stop(): void;
  dispose(): void;
};

export type SpeechRecognitionOptions = {
  button: HTMLButtonElement;
  input: HTMLElement;
  onError?: (message: string) => void;
  createRecognition?: () => SpeechRecognitionLike;
};

type TranscriptTarget = {
  prefix: Text | null;
  transcript: HTMLSpanElement;
  suffix: Text | null;
};

function recognitionErrorMessage(error: SpeechRecognitionError): string {
  switch (error) {
    case "not-allowed":
    case "service-not-allowed":
      return "Microphone access was denied. Allow microphone access for Echo, then try again.";
    case "audio-capture":
      return "Echo could not find an available microphone.";
    case "no-speech":
      return "No speech was detected. Try speaking closer to the microphone.";
    case "network":
      return "Voice recognition is unavailable because the speech service could not be reached.";
    case "language-not-supported":
      return "Voice recognition does not support the browser's current language.";
    default:
      return "Voice recognition stopped unexpectedly. Please try again.";
  }
}

function activeRange(input: HTMLElement): Range {
  const selection = window.getSelection();
  if (selection?.rangeCount) {
    const selected = selection.getRangeAt(0);
    if (input.contains(selected.commonAncestorContainer)) return selected.cloneRange();
  }
  const range = document.createRange();
  range.selectNodeContents(input);
  range.collapse(false);
  return range;
}

function textAroundRange(input: HTMLElement, range: Range): { before: string; after: string } {
  const before = document.createRange();
  before.selectNodeContents(input);
  before.setEnd(range.startContainer, range.startOffset);
  const after = document.createRange();
  after.selectNodeContents(input);
  after.setStart(range.endContainer, range.endOffset);
  return { before: before.toString(), after: after.toString() };
}

function createTranscriptTarget(input: HTMLElement): TranscriptTarget {
  const range = activeRange(input);
  const { before, after } = textAroundRange(input, range);
  const prefix = before && !/\s$/.test(before) ? document.createTextNode(" ") : null;
  const suffix = after && !/^\s/.test(after) ? document.createTextNode(" ") : null;
  const transcript = document.createElement("span");
  transcript.className = "chat-voice-transcript is-interim";
  transcript.dataset.chatVoiceTranscript = "";

  range.deleteContents();
  const fragment = document.createDocumentFragment();
  if (prefix) fragment.append(prefix);
  fragment.append(transcript);
  if (suffix) fragment.append(suffix);
  range.insertNode(fragment);
  return { prefix, transcript, suffix };
}

function placeCaretAfter(node: Node): void {
  const selection = window.getSelection();
  if (!selection || !node.parentNode) return;
  const range = document.createRange();
  range.setStartAfter(node);
  range.collapse(true);
  selection.removeAllRanges();
  selection.addRange(range);
}

/**
 * Adds click-to-start/click-to-stop browser speech recognition to a chat
 * composer. Recognition text is inserted at the current caret and stays in
 * the composer for review; this controller never submits the prompt.
 */
export function mountSpeechRecognition(options: SpeechRecognitionOptions): SpeechRecognitionController {
  const { button, input, onError } = options;
  const speechWindow = window as SpeechRecognitionWindow;
  const Recognition = speechWindow.SpeechRecognition || speechWindow.webkitSpeechRecognition;
  const createRecognition = options.createRecognition || (Recognition ? () => new Recognition() : null);

  button.setAttribute("aria-pressed", "false");
  if (!createRecognition) {
    button.disabled = true;
    button.title = "Voice input is not supported by this browser";
    button.setAttribute("aria-label", "Voice input unavailable");
    button.classList.add("is-unavailable");
    return { stop() {}, dispose() {} };
  }

  const recognition = createRecognition();
  recognition.continuous = true;
  recognition.interimResults = true;
  recognition.lang = navigator.language || "en-US";
  recognition.maxAlternatives = 1;

  let active = false;
  let starting = false;
  let disposed = false;
  let intentionalStop = false;
  let finalTranscript = "";
  let target: TranscriptTarget | null = null;

  const setButtonState = (state: "idle" | "starting" | "listening" | "stopping") => {
    const engaged = state !== "idle";
    button.classList.toggle("is-starting", state === "starting");
    button.classList.toggle("is-listening", state === "listening");
    button.classList.toggle("is-stopping", state === "stopping");
    button.setAttribute("aria-pressed", String(engaged));
    if (state === "idle") {
      button.title = "Start voice input";
      button.setAttribute("aria-label", "Start voice input");
    } else if (state === "starting") {
      button.title = "Starting microphone…";
      button.setAttribute("aria-label", "Cancel voice input");
    } else if (state === "listening") {
      button.title = "Stop voice input";
      button.setAttribute("aria-label", "Stop voice input");
    } else {
      button.title = "Stopping microphone…";
      button.setAttribute("aria-label", "Stopping voice input");
    }
  };

  const updateTranscript = (interimTranscript = "") => {
    const text = [finalTranscript.trim(), interimTranscript.trim()].filter(Boolean).join(" ");
    if (!text) return;
    if (target && !input.contains(target.transcript)) return;
    target ||= createTranscriptTarget(input);
    target.transcript.textContent = text;
    target.transcript.classList.toggle("is-interim", Boolean(interimTranscript.trim()));
    placeCaretAfter(target.transcript);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  };

  const finalizeTranscript = () => {
    if (!target) return;
    const { prefix, transcript, suffix } = target;
    if (!input.contains(transcript)) {
      target = null;
      return;
    }
    const text = transcript.textContent || "";
    if (!text) {
      prefix?.remove();
      suffix?.remove();
      transcript.remove();
    } else {
      const textNode = document.createTextNode(text);
      transcript.replaceWith(textNode);
      placeCaretAfter(textNode);
    }
    target = null;
    input.dispatchEvent(new Event("input", { bubbles: true }));
  };

  const reset = () => {
    active = false;
    starting = false;
    finalTranscript = "";
    setButtonState("idle");
    finalizeTranscript();
  };

  recognition.onstart = () => {
    if (disposed) return;
    active = true;
    starting = false;
    setButtonState("listening");
  };

  recognition.onresult = (event) => {
    if (disposed) return;
    let interimTranscript = "";
    for (let index = event.resultIndex; index < event.results.length; index += 1) {
      const result = event.results[index];
      const transcript = result?.[0]?.transcript || "";
      if (result?.isFinal) finalTranscript = [finalTranscript, transcript.trim()].filter(Boolean).join(" ");
      else interimTranscript = [interimTranscript, transcript.trim()].filter(Boolean).join(" ");
    }
    updateTranscript(interimTranscript);
  };

  recognition.onerror = (event) => {
    if (disposed || (intentionalStop && event.error === "aborted")) return;
    if (event.error !== "aborted") onError?.(recognitionErrorMessage(event.error));
  };

  recognition.onend = () => {
    if (disposed) return;
    intentionalStop = false;
    reset();
  };

  const start = () => {
    intentionalStop = false;
    finalTranscript = "";
    target = null;
    starting = true;
    setButtonState("starting");
    try {
      recognition.start();
    } catch {
      reset();
      onError?.("Voice recognition could not start. Please try again.");
    }
  };

  const stopRecognition = () => {
    intentionalStop = true;
    setButtonState("stopping");
    try {
      if (active) recognition.stop();
      else recognition.abort();
    } catch {
      reset();
    }
  };

  const onClick = () => {
    if (active || starting) stopRecognition();
    else start();
  };

  setButtonState("idle");
  button.addEventListener("click", onClick);

  return {
    stop() {
      if (active || starting) stopRecognition();
    },
    dispose() {
      if (disposed) return;
      disposed = true;
      button.removeEventListener("click", onClick);
      recognition.onstart = null;
      recognition.onresult = null;
      recognition.onerror = null;
      recognition.onend = null;
      if (active || starting) {
        try { recognition.abort(); } catch { /* Recognition may already be ending. */ }
      }
      finalizeTranscript();
    },
  };
}
