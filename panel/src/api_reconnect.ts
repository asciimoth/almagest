export const API_RECONNECT_INITIAL_DELAY_MS = 1_000;
export const API_RECONNECT_MAX_DELAY_MS = 30_000;

export function apiReconnectDelay(attempt: number): number {
  const exponent = Math.max(0, Math.floor(attempt));
  return Math.min(
    API_RECONNECT_INITIAL_DELAY_MS * (2 ** exponent),
    API_RECONNECT_MAX_DELAY_MS,
  );
}
