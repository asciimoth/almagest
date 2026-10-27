// Scope the name to one daemon origin so that separate Almagest instances do not share a tab.
export function adminPanelWindowName(origin: string): string {
  return `almagest-admin-panel:${origin}`;
}

export function openOrFocusAdminPanel(source: Window, panelURL: string): boolean {
  // An empty URL selects an existing named context without navigating it. If no
  // context has this name, the browser creates one with about:blank instead.
  const panel = source.open("", adminPanelWindowName(source.location.origin));
  if (!panel) return false;

  // Navigate only a new context. An existing SPA keeps its current state.
  if (panel.location.href === "about:blank") panel.location.replace(panelURL);
  panel.focus();
  return true;
}

/** Open a panel route, or navigate the existing named panel to that route. */
export function openAdminPanelRoute(source: Window, panelURL: string): boolean {
  // Supplying the URL makes the browser apply its normal named-target
  // navigation. A same-document fragment changes the SPA tab without reload.
  const panel = source.open(panelURL, adminPanelWindowName(source.location.origin));
  if (!panel) return false;
  panel.focus();
  return true;
}
