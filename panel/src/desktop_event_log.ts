export type DesktopEvent =
  | "docs_frame_created"
  | "docs_frame_loaded"
  | "docs_frame_ready"
  | "docs_frame_reconnected"
  | "docs_frame_timeout";

let desktopEventLogger: ((event: DesktopEvent) => void) | null = null;

export function configureDesktopEventLogger(logger: (event: DesktopEvent) => void): void {
  desktopEventLogger = logger;
}

/** Write one fixed diagnostic event when the native logger is available. */
export function logDesktopEvent(event: DesktopEvent): void {
  desktopEventLogger?.(event);
}
