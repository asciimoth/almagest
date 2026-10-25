export const API_RECONNECT_INITIAL_DELAY_MS = 1_000;
export const API_RECONNECT_MAX_DELAY_MS = 30_000;

export function apiReconnectDelay(attempt: number, visible = true): number {
  // A visible local admin panel must recover promptly when the isolated daemon
  // publishes its host-facing listener. Keep exponential backoff for hidden
  // tabs so they do not make frequent connection attempts while the daemon is
  // intentionally offline.
  if (visible) return API_RECONNECT_INITIAL_DELAY_MS;
  const exponent = Math.max(0, Math.floor(attempt));
  return Math.min(
    API_RECONNECT_INITIAL_DELAY_MS * (2 ** exponent),
    API_RECONNECT_MAX_DELAY_MS,
  );
}
