export type GraphRectangle = readonly [x: number, y: number, width: number, height: number];

export type GraphViewport = {
  scale: number;
  offset: [number, number];
};

export type GraphViewportPadding = {
  top: number;
  right: number;
  bottom: number;
  left: number;
};

export function fitGraphRectangles(
  rectangles: readonly GraphRectangle[],
  viewportWidth: number,
  viewportHeight: number,
  padding: number | GraphViewportPadding,
  maxScale: number,
  fixedScale?: number,
): GraphViewport | null {
  if (!rectangles.length || viewportWidth <= 0 || viewportHeight <= 0) return null;

  let minX = Number.POSITIVE_INFINITY;
  let minY = Number.POSITIVE_INFINITY;
  let maxX = Number.NEGATIVE_INFINITY;
  let maxY = Number.NEGATIVE_INFINITY;
  for (const [x, y, width, height] of rectangles) {
    if (![x, y, width, height].every(Number.isFinite)) continue;
    minX = Math.min(minX, x);
    minY = Math.min(minY, y);
    maxX = Math.max(maxX, x + Math.max(0, width));
    maxY = Math.max(maxY, y + Math.max(0, height));
  }
  if (![minX, minY, maxX, maxY].every(Number.isFinite)) return null;

  const resolvedPadding = typeof padding === "number"
    ? { top: padding, right: padding, bottom: padding, left: padding }
    : padding;
  const top = Math.max(0, resolvedPadding.top);
  const right = Math.max(0, resolvedPadding.right);
  const bottom = Math.max(0, resolvedPadding.bottom);
  const left = Math.max(0, resolvedPadding.left);
  const contentWidth = Math.max(1, maxX - minX);
  const contentHeight = Math.max(1, maxY - minY);
  const availableWidth = Math.max(1, viewportWidth - left - right);
  const availableHeight = Math.max(1, viewportHeight - top - bottom);
  const upperScale = Math.max(Number.EPSILON, maxScale);
  const autoScale = Math.min(upperScale,
    Math.max(Number.EPSILON, Math.min(availableWidth / contentWidth, availableHeight / contentHeight)));
  const scale = fixedScale === undefined ? autoScale : Math.max(Number.EPSILON, fixedScale);
  const centerX = minX + (maxX - minX) * 0.5;
  const centerY = minY + (maxY - minY) * 0.5;
  const viewportCenterX = left + availableWidth * 0.5;
  const viewportCenterY = top + availableHeight * 0.5;

  return {
    scale,
    offset: [viewportCenterX / scale - centerX, viewportCenterY / scale - centerY],
  };
}
