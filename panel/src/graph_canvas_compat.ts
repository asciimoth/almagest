/** The small LiteGraph surface used by the desktop wheel compatibility code. */
export interface ZoomableGraphCanvas {
  allow_dragcanvas: boolean;
  ds: {
    scale: number;
    changeScale(scale: number, center: [number, number]): void;
  };
  graph: { change(): void } | null;
}

/** The small LiteGraph surface needed when a hidden canvas becomes visible. */
export interface DrawableGraphCanvas {
  setDirty(foreground: boolean, background: boolean): void;
  draw(forceForeground: boolean, forceBackground: boolean): void;
}

/**
 * Apply one standard WheelEvent to LiteGraph.
 *
 * LiteGraph 0.7 listens for legacy `mousewheel` events. WebKitGTK sends the
 * standard `wheel` event instead, so the Tauri panel supplies this adapter.
 * The zoom center uses canvas-local coordinates, as DragAndScale requires.
 */
export function applyStandardWheelZoom(
  canvas: ZoomableGraphCanvas,
  element: Pick<HTMLElement, "getBoundingClientRect">,
  event: Pick<WheelEvent, "clientX" | "clientY" | "deltaY" | "preventDefault">,
): boolean {
  if (!canvas.graph || !canvas.allow_dragcanvas || !Number.isFinite(event.deltaY) || event.deltaY === 0) {
    return false;
  }

  const rect = element.getBoundingClientRect();
  const factor = event.deltaY < 0 ? 1.1 : 1 / 1.1;
  canvas.ds.changeScale(canvas.ds.scale * factor, [event.clientX - rect.left, event.clientY - rect.top]);
  canvas.graph.change();
  event.preventDefault();
  return true;
}

/** Force a complete frame after the graph panel returns from `display: none`. */
export function redrawRevealedGraph(canvas: DrawableGraphCanvas): void {
  canvas.setDirty(true, true);
  canvas.draw(true, true);
}
