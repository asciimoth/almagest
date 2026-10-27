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

export interface PinchZoomGraphCanvas {
  allow_dragcanvas: boolean;
  ds: {
    scale: number;
    offset: [number, number] | Float32Array;
    changeScale(scale: number, center: [number, number]): void;
  };
  graph: { change(): void } | null;
}

type TouchPointer = Pick<PointerEvent, "pointerId" | "pointerType" | "clientX" | "clientY">;

/** Add the two-pointer zoom behavior that LiteGraph 0.7 does not implement. */
export class GraphPinchZoom {
  private readonly pointers = new Map<number, { x: number; y: number }>();
  private pinching = false;
  private previousCenter: [number, number] | null = null;
  private previousDistance = 0;

  constructor(
    private readonly canvas: PinchZoomGraphCanvas,
    private readonly element: Pick<HTMLElement, "getBoundingClientRect">,
  ) {}

  start(event: TouchPointer): boolean {
    if (event.pointerType !== "touch" || !this.canvas.allow_dragcanvas) return false;
    this.pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (this.pointers.size < 2) return this.pinching;
    this.pinching = true;
    this.rememberGeometry();
    return true;
  }

  move(event: TouchPointer): boolean {
    if (event.pointerType !== "touch" || !this.pointers.has(event.pointerId)) return false;
    this.pointers.set(event.pointerId, { x: event.clientX, y: event.clientY });
    if (!this.pinching || this.pointers.size < 2) return this.pinching;

    const geometry = this.geometry();
    if (!geometry) return true;
    if (!this.previousCenter || this.previousDistance <= 0) {
      this.previousCenter = geometry.center;
      this.previousDistance = geometry.distance;
      return true;
    }
    const rect = this.element.getBoundingClientRect();
    const oldCenter: [number, number] = [
      this.previousCenter[0] - rect.left,
      this.previousCenter[1] - rect.top,
    ];
    this.canvas.ds.changeScale(this.canvas.ds.scale * geometry.distance / this.previousDistance, oldCenter);
    this.canvas.ds.offset[0] += (geometry.center[0] - this.previousCenter[0]) / this.canvas.ds.scale;
    this.canvas.ds.offset[1] += (geometry.center[1] - this.previousCenter[1]) / this.canvas.ds.scale;
    this.previousCenter = geometry.center;
    this.previousDistance = geometry.distance;
    this.canvas.graph?.change();
    return true;
  }

  end(event: TouchPointer): boolean {
    if (event.pointerType !== "touch" || !this.pointers.has(event.pointerId)) return false;
    const handled = this.pinching;
    this.pointers.delete(event.pointerId);
    if (this.pointers.size === 0) {
      this.pinching = false;
      this.previousCenter = null;
      this.previousDistance = 0;
    } else if (this.pointers.size >= 2) {
      this.rememberGeometry();
    }
    return handled;
  }

  cancel(): void {
    this.pointers.clear();
    this.pinching = false;
    this.previousCenter = null;
    this.previousDistance = 0;
  }

  private rememberGeometry(): void {
    const geometry = this.geometry();
    this.previousCenter = geometry?.center ?? null;
    this.previousDistance = geometry?.distance ?? 0;
  }

  private geometry(): { center: [number, number]; distance: number } | null {
    const [first, second] = [...this.pointers.values()];
    if (!first || !second) return null;
    return {
      center: [(first.x + second.x) / 2, (first.y + second.y) / 2],
      distance: Math.hypot(second.x - first.x, second.y - first.y),
    };
  }
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
