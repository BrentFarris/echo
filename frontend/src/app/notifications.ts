
import notificationSoundUrl from "../../notification-1.wav";
import { state, notificationSoundsEnabled } from "./state";

const notificationSound = new Audio(notificationSoundUrl);
notificationSound.preload = "auto";

export function playNotificationSound() {
  if (!notificationSoundsEnabled(state.appState?.settings ?? state.settingsDraft)) {
    return;
  }
  const audio = notificationSound.cloneNode(true) as HTMLAudioElement;
  void audio.play().catch(() => {
    // Browsers can reject audio until the user has interacted with the app.
  });
}
