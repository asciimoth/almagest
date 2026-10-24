export type PanelMode = "admin" | "webdemo";

const WEB_DEMO_EXCLUDED_TABS = new Set(["tls", "snapshots"]);

export function setPanelMode(root: HTMLElement, mode: PanelMode): void {
  root.dataset.almagestPanelMode = mode;
}

export function panelMode(root: HTMLElement): PanelMode {
  return root.dataset.almagestPanelMode === "webdemo" ? "webdemo" : "admin";
}

export function applyPanelMode(document: Document, mode: PanelMode): void {
  const webDemo = mode === "webdemo";
  for (const tab of document.querySelectorAll<HTMLElement>("[data-tab]")) {
    tab.hidden = webDemo && WEB_DEMO_EXCLUDED_TABS.has(tab.dataset.tab ?? "");
  }
  for (const panel of document.querySelectorAll<HTMLElement>("[data-panel]")) {
    panel.hidden = webDemo && WEB_DEMO_EXCLUDED_TABS.has(panel.dataset.panel ?? "");
  }
  const runtimeStatus = document.querySelector<HTMLElement>("#ps-runtime-status");
  if (runtimeStatus) {
    runtimeStatus.hidden = webDemo;
  }
}

export function showsRuntimeSecurityStatus(mode: PanelMode): boolean {
  return mode === "admin";
}
