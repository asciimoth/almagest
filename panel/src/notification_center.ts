import { text as localizedText } from "./localization.js";

export type NotificationTone = "success" | "info" | "warning" | "error";

export type NotificationOptions = {
  title?: string;
  tone?: NotificationTone;
  duration?: number;
};

type NotificationEntry = {
  element: HTMLDivElement;
  timer: number;
};

function defaultTitle(tone: NotificationTone): string {
  switch (tone) {
    case "success": return localizedText("notification.success.title", "Done");
    case "info": return localizedText("notification.info.title", "Notice");
    case "warning": return localizedText("notification.warning.title", "Attention");
    case "error": return localizedText("notification.error.title", "Action failed");
  }
}

// Keep transient operation feedback in one place. The center owns stacking,
// dismissal, timeouts, duplicate suppression, and accessible live-region roles.
export class NotificationCenter {
  private readonly entries = new Map<number, NotificationEntry>();
  private nextID = 1;

  constructor(
    private readonly container: HTMLElement,
    private readonly hostWindow: Window,
    private readonly maximumVisible = 4,
  ) {}

  show(message: string, options: NotificationOptions = {}): number {
    const tone = options.tone ?? "info";
    const title = options.title ?? defaultTitle(tone);
    const duration = options.duration ?? (tone === "error" ? 10_000 : 5_000);
    const duplicate = this.findDuplicate(title, message, tone);
    if (duplicate !== null) this.dismiss(duplicate);

    const id = this.nextID;
    this.nextID += 1;
    const element = createNotificationToast(this.container.ownerDocument, message, { title, tone });
    element.dataset.notificationId = String(id);
    element.querySelector("button")?.addEventListener("click", () => this.dismiss(id));
    this.container.append(element);

    const timer = duration > 0
      ? this.hostWindow.setTimeout(() => this.dismiss(id), duration)
      : 0;
    this.entries.set(id, { element, timer });
    this.trim();
    return id;
  }

  dismiss(id: number): void {
    const entry = this.entries.get(id);
    if (!entry) return;
    if (entry.timer) this.hostWindow.clearTimeout(entry.timer);
    entry.element.remove();
    this.entries.delete(id);
  }

  clear(): void {
    for (const id of [...this.entries.keys()]) this.dismiss(id);
  }

  private findDuplicate(title: string, message: string, tone: NotificationTone): number | null {
    for (const [id, entry] of this.entries) {
      if (
        entry.element.dataset.notificationTitle === title
        && entry.element.dataset.notificationMessage === message
        && entry.element.dataset.notificationTone === tone
      ) return id;
    }
    return null;
  }

  private trim(): void {
    while (this.entries.size > this.maximumVisible) {
      const oldest = this.entries.keys().next().value as number | undefined;
      if (oldest === undefined) return;
      this.dismiss(oldest);
    }
  }
}

export function createNotificationToast(
  document: Document,
  message: string,
  options: Pick<NotificationOptions, "title" | "tone"> = {},
): HTMLDivElement {
  const tone = options.tone ?? "info";
  const title = options.title ?? defaultTitle(tone);
  const toast = document.createElement("div");
  toast.className = `notification notification-${tone}`;
  toast.dataset.notificationTitle = title;
  toast.dataset.notificationMessage = message;
  toast.dataset.notificationTone = tone;
  toast.setAttribute("role", tone === "error" ? "alert" : "status");
  toast.setAttribute("aria-atomic", "true");

  const copy = document.createElement("div");
  copy.className = "notification-copy";
  const heading = document.createElement("strong");
  heading.textContent = title;
  const body = document.createElement("span");
  body.textContent = message;
  copy.append(heading, body);

  const dismiss = document.createElement("button");
  dismiss.type = "button";
  dismiss.className = "icon-button notification-dismiss";
  dismiss.textContent = "×";
  dismiss.title = localizedText("notification.dismiss.title", "Dismiss");
  dismiss.setAttribute("aria-label", localizedText("notification.dismiss.aria_label", "Dismiss notification"));
  toast.append(copy, dismiss);
  return toast;
}
