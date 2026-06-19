import * as WailsRuntime from "../../wailsjs/runtime/runtime";
import { isWailsRuntime, webEventsOn } from "./web";

export function EventsOn(eventName: string, callback: (event: any) => void) {
  if (isWailsRuntime()) {
    return WailsRuntime.EventsOn(eventName, callback);
  }
  return webEventsOn(eventName, callback);
}
