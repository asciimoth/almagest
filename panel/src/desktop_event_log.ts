import { invoke } from "@tauri-apps/api/core";

export type DesktopEvent =
  | "docs_frame_created"
  | "docs_frame_loaded"
  | "docs_frame_ready"
  | "docs_frame_reconnected"
  | "docs_frame_timeout";

/** Write one fixed diagnostic event to the native desktop application log. */
export function logDesktopEvent(event: DesktopEvent): void {
  void invoke("log_desktop_event", { event }).catch((error: unknown) => {
    console.warn(`[almagest-desktop] could not log ${event}`, error);
  });
}
