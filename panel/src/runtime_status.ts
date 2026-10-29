/** Show only runtime status controls for which the hello response has details. */
export function setRuntimeStatusVisibility(
  container: HTMLElement,
  isolation: HTMLElement,
  sandbox: HTMLElement,
  hasIsolation: boolean,
  hasSandbox: boolean,
): void {
  isolation.hidden = !hasIsolation;
  sandbox.hidden = !hasSandbox;
  container.hidden = !hasIsolation && !hasSandbox;
}
