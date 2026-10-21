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
}
