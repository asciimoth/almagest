import { text as localizedText } from "./localization.js";

export type RegularSnapshotInfo = {
  uuid: string;
  timestamp: number;
  name?: string;
  comment?: never;
  example?: never;
};

export type ExampleSnapshotInfo = {
  uuid?: never;
  timestamp?: never;
  name: string;
  comment: string;
  example: string;
};

export type SnapshotInfo = RegularSnapshotInfo | ExampleSnapshotInfo;

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
  if (!snapshot.uuid) return entries;
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
    empty.textContent = localizedText("snapshots.empty", "No snapshots are available.");
    list.append(empty);
    return;
  }
  const regular = entries.filter((entry) => !entry.example);
  const examples = entries.filter((entry) => entry.example);
  for (const [index, entry] of [...regular, ...examples].entries()) {
    if (entry.example && index === regular.length) {
      const delimiter = document.createElement("li");
      delimiter.className = "snapshot-examples-delimiter";
      delimiter.textContent = localizedText("snapshots.examples", "Examples");
      list.append(delimiter);
    }
    const row = document.createElement("li");
    row.className = entry.example ? "snapshot-row snapshot-example-row" : "snapshot-row";
    const copy = document.createElement("div");
    copy.className = "snapshot-copy";
    copy.append(textElement(document, "strong", entry.name || localizedText("snapshots.unnamed", "Unnamed snapshot")));
    if (entry.example) {
      if (entry.comment) copy.append(textElement(document, "span", entry.comment, "snapshot-meta"));
    } else {
      copy.append(
        textElement(document, "code", entry.uuid ?? ""),
        textElement(document, "span", formatSnapshotTimestamp(entry.timestamp ?? Number.NaN), "snapshot-meta"),
      );
    }
    const rowActions = document.createElement("div");
    rowActions.className = "snapshot-row-actions";
    if (entry.example) {
      rowActions.append(actionButton(document, localizedText("action.load", "Load"), "snapshot-load", disabled, () => actions.restore(entry)));
    } else {
      rowActions.append(
        actionButton(document, localizedText("action.export", "Export"), "snapshot-export", disabled, () => actions.export(entry)),
        actionButton(document, localizedText("action.rename", "Rename"), "snapshot-rename", disabled, () => actions.rename(entry)),
        actionButton(document, localizedText("action.restore", "Restore"), "snapshot-restore", disabled, () => actions.restore(entry)),
        actionButton(document, localizedText("action.delete", "Delete"), "snapshot-delete danger", disabled, () => actions.delete(entry)),
      );
    }
    row.append(copy, rowActions);
    list.append(row);
  }
}

export function formatSnapshotTimestamp(timestamp: number): string {
  const date = new Date(Math.trunc(timestamp / 1_000_000));
  if (Number.isNaN(date.getTime())) return localizedText("common.invalid_date", "Invalid date");
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
