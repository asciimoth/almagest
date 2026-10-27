export function initialTopicOpen(
  folded: boolean,
  savedOpen: boolean | undefined,
  containsCurrentPage: boolean,
): boolean {
  return containsCurrentPage || (savedOpen ?? !folded);
}

export function selectPageInTree(root: ParentNode, id: string): void {
  let activeLink: HTMLElement | null = null;
  for (const link of root.querySelectorAll<HTMLElement>("[data-page-id]")) {
    const active = link.dataset.pageId === id;
    link.classList.toggle("active", active);
    if (active) activeLink = link;
  }

  let topic = activeLink?.closest<HTMLDetailsElement>("details") ?? null;
  while (topic) {
    topic.open = true;
    topic = topic.parentElement?.closest<HTMLDetailsElement>("details") ?? null;
  }

  if (activeLink && isScrollContainer(root)) revealTreeItem(root, activeLink);
}

function isScrollContainer(value: ParentNode): value is HTMLElement {
  const candidate = value as Partial<HTMLElement>;
  return typeof candidate.getBoundingClientRect === "function"
    && typeof candidate.clientHeight === "number"
    && typeof candidate.scrollTop === "number";
}

/**
 * Move a scrollable documentation tree by the smallest distance that makes an
 * item fully visible. Keep the user's position unchanged when the item is
 * already visible.
 */
export function revealTreeItem(container: HTMLElement, item: HTMLElement): boolean {
  const containerBounds = container.getBoundingClientRect();
  const itemBounds = item.getBoundingClientRect();
  const viewportTop = containerBounds.top;
  const viewportBottom = viewportTop + container.clientHeight;
  let delta = 0;

  if (itemBounds.top < viewportTop) delta = itemBounds.top - viewportTop;
  else if (itemBounds.bottom > viewportBottom) delta = itemBounds.bottom - viewportBottom;

  if (delta === 0) return false;
  container.scrollTop += delta;
  return true;
}

type ScrollContainer = Pick<HTMLElement, "clientHeight" | "scrollHeight" | "scrollTop">;

export function handoffBoundaryScroll(
  source: ScrollContainer,
  destination: ScrollContainer,
  deltaY: number,
): boolean {
  if (deltaY === 0) return false;

  const maximumScrollTop = Math.max(0, source.scrollHeight - source.clientHeight);
  const atBoundary = deltaY < 0
    ? source.scrollTop <= 0
    : source.scrollTop >= maximumScrollTop - 1;
  if (!atBoundary) return false;

  destination.scrollTop += deltaY;
  return true;
}
