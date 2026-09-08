export type SnapshotInfo = {
  uuid: string;
  timestamp: number;
  name?: string;
};

// Snapshot archives use base64 inside the shared JSON API. Keep this value in
// step with api.SnapshotArchiveMaxBytes so the panel rejects oversized files
// before FileReader creates another complete in-memory copy.
export const SNAPSHOT_ARCHIVE_MAX_BYTES = 32 * 1024 * 1024;

export type SnapshotRowActions = {
  export(entry: SnapshotInfo): void;
  rename(entry: SnapshotInfo): void;
  restore(entry: SnapshotInfo): void;
  delete(entry: SnapshotInfo): void;
};

export function upsertSnapshot(entries: SnapshotInfo[], snapshot: SnapshotInfo): SnapshotInfo[] {
  const index = entries.findIndex((entry) => entry.uuid === snapshot.uuid);
  if (index < 0) return [snapshot, ...entries];
  return entries.map((entry, entryIndex) => entryIndex === index ? snapshot : entry);
}

export function removeSnapshot(entries: SnapshotInfo[], uuid: string): SnapshotInfo[] {
  return entries.filter((entry) => entry.uuid !== uuid);
}

export function renderSnapshotList(
  list: HTMLOListElement,
  entries: SnapshotInfo[],
  disabled: boolean,
  actions: SnapshotRowActions,
): void {
  const document = list.ownerDocument;
  list.replaceChildren();
  if (!entries.length) {
    const empty = document.createElement("li");
    empty.className = "snapshot-empty";
    empty.textContent = "No snapshots are available.";
    list.append(empty);
    return;
  }
  for (const entry of entries) {
    const row = document.createElement("li");
    row.className = "snapshot-row";
    const copy = document.createElement("div");
    copy.className = "snapshot-copy";
    copy.append(
      textElement(document, "strong", entry.name || "Unnamed snapshot"),
      textElement(document, "code", entry.uuid),
      textElement(document, "span", formatSnapshotTimestamp(entry.timestamp), "snapshot-meta"),
    );
    const rowActions = document.createElement("div");
    rowActions.className = "snapshot-row-actions";
    rowActions.append(
      actionButton(document, "Export", "snapshot-export", disabled, () => actions.export(entry)),
      actionButton(document, "Rename", "snapshot-rename", disabled, () => actions.rename(entry)),
      actionButton(document, "Restore", "snapshot-restore", disabled, () => actions.restore(entry)),
      actionButton(document, "Delete", "snapshot-delete danger", disabled, () => actions.delete(entry)),
    );
    row.append(copy, rowActions);
    list.append(row);
  }
}

export function formatSnapshotTimestamp(timestamp: number): string {
  const date = new Date(Math.trunc(timestamp / 1_000_000));
  if (Number.isNaN(date.getTime())) return "Invalid date";
  return date.toLocaleString();
}

function actionButton(
  document: Document,
  label: string,
  className: string,
  disabled: boolean,
  action: () => void,
): HTMLButtonElement {
  const button = document.createElement("button");
  button.type = "button";
  button.className = className;
  button.textContent = label;
  button.disabled = disabled;
  button.addEventListener("click", action);
  return button;
}

function textElement(document: Document, tag: string, text: string, className = ""): HTMLElement {
  const element = document.createElement(tag);
  element.textContent = text;
  if (className) element.className = className;
  return element;
}
