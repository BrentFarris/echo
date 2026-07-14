import * as WailsRuntime from "../../wailsjs/runtime/runtime";
import { isWailsRuntime, webEventsOn } from "./web";

export function EventsOn(eventName: string, callback: (event: any) => void) {
  if (isWailsRuntime()) {
    return WailsRuntime.EventsOn(eventName, callback);
  }
  return webEventsOn(eventName, callback);
}

export function OnFileDrop(callback: (x: number, y: number, paths: string[]) => void) {
  if (isWailsRuntime()) {
    WailsRuntime.OnFileDrop(callback, false);
  }
}

export function SetWindowTitle(title: string) {
  document.title = title;
  if (isWailsRuntime()) {
    WailsRuntime.WindowSetTitle(title);
  }
}
