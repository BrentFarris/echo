import questionSoundUrl from "../question-1.wav";
import { get } from "../js/api.js";

type PlanQuestionSoundSettings = {
  disablePlanQuestionSounds?: boolean;
};

let soundsEnabled = true;

const questionSound = typeof Audio === "function" ? new Audio(questionSoundUrl) : null;
if (questionSound) questionSound.preload = "auto";

function settingsValue(value: unknown): PlanQuestionSoundSettings {
  if (value && typeof value === "object" && "settings" in value) {
    return ((value as { settings?: PlanQuestionSoundSettings }).settings || {});
  }
  return (value as PlanQuestionSoundSettings | null) || {};
}

export function updatePlanQuestionSoundSettings(value: unknown): void {
  const settings = settingsValue(value);
  soundsEnabled = settings.disablePlanQuestionSounds !== true;
}

export async function refreshPlanQuestionSoundSettings(): Promise<void> {
  try {
    updatePlanQuestionSoundSettings(await get("/api/settings"));
  } catch (error) {
    console.warn("failed to load plan question sound settings", error);
  }
}

export async function startPlanQuestionSound(): Promise<void> {
  await refreshPlanQuestionSoundSettings();
}

export function playPlanQuestionSound(): void {
  if (!soundsEnabled || !questionSound) return;
  const audio = questionSound.cloneNode(true) as HTMLAudioElement;
  void audio.play().catch(() => {
    // Browsers can reject audio before the user has interacted with Echo.
  });
}
