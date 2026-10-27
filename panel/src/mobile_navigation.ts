// Keep the same navigation controls and URL history on every screen size.
export function setupMobileNavigation(doc: Document, compact: MediaQueryList): void {
  const toggle = doc.querySelector<HTMLButtonElement>("#mobile-navigation-toggle")!;
  const scrim = doc.querySelector<HTMLButtonElement>("#mobile-navigation-scrim")!;
  const nav = doc.querySelector<HTMLElement>("#panel-navigation")!;
  const label = doc.querySelector<HTMLElement>("#mobile-current-section")!;
  const panels = [...doc.querySelectorAll<HTMLElement>("[data-panel]")];
  const brand = doc.querySelector<HTMLElement>(".app-brand")!;
  let open = false;

  function syncLabel(): void {
    label.textContent = nav.querySelector('[aria-selected="true"]')?.getAttribute("aria-label") ?? "";
  }

  function setOpen(value: boolean, restoreFocus = false): void {
    open = value && compact.matches;
    nav.classList.toggle("mobile-open", open);
    toggle.setAttribute("aria-expanded", String(open));
    scrim.hidden = !open;
    for (const panel of panels) panel.inert = open;
    brand.inert = open;
    if (open) nav.querySelector<HTMLElement>('[aria-selected="true"]')?.focus();
    else if (restoreFocus) toggle.focus();
  }

  toggle.addEventListener("click", () => setOpen(!open));
  scrim.addEventListener("click", () => setOpen(false, true));
  nav.addEventListener("click", (event) => {
    if ((event.target as Element).closest("button") && open) setOpen(false, true);
  });
  doc.addEventListener("keydown", (event) => {
    if (!open) return;
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopImmediatePropagation();
      setOpen(false, true);
    } else if (event.key === "Tab") {
      const controls = [...nav.querySelectorAll<HTMLElement>('button:not([hidden]):not(:disabled), select:not(:disabled)')];
      controls.push(toggle);
      const index = controls.indexOf(doc.activeElement as HTMLElement);
      const next = (index + (event.shiftKey ? -1 : 1) + controls.length) % controls.length;
      event.preventDefault();
      controls[next]?.focus();
    }
  }, true);
  compact.addEventListener("change", () => {
    const focused = doc.activeElement;
    setOpen(false);
    if (compact.matches && focused && nav.contains(focused)) toggle.focus();
    else if (!compact.matches && focused === toggle) nav.querySelector<HTMLElement>('[aria-selected="true"]')?.focus();
  });
  doc.defaultView?.addEventListener("hashchange", () => {
    if (open) setOpen(false, true);
  });
  new MutationObserver(syncLabel).observe(nav, {
    subtree: true, attributes: true, attributeFilter: ["aria-label", "aria-selected"],
  });
  syncLabel();
}
