import assert from "node:assert/strict";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import { NotificationCenter, createNotificationToast } from "../public/build/notification_center.js";

test("notification toasts render untrusted text and accessible status", () => {
  const dom = new JSDOM("<div id='notifications'></div>");
  const payload = "<img src=x onerror=alert(1)>";
  const toast = createNotificationToast(dom.window.document, payload, {
    title: "Copied",
    tone: "success",
  });

  assert.equal(toast.querySelector("img"), null);
  assert.equal(toast.querySelector("span")?.textContent, payload);
  assert.equal(toast.querySelector("strong")?.textContent, "Copied");
  assert.equal(toast.getAttribute("role"), "status");
  assert.equal(toast.classList.contains("notification-success"), true);
});

test("notification center deduplicates, limits, and dismisses notifications", () => {
  const dom = new JSDOM("<div id='notifications'></div>");
  const root = dom.window.document.querySelector("#notifications");
  const center = new NotificationCenter(root, dom.window, 2);

  center.show("First", { duration: 0 });
  center.show("First", { duration: 0 });
  assert.equal(root.children.length, 1);

  center.show("Second", { duration: 0 });
  center.show("Third", { duration: 0 });
  assert.deepEqual([...root.querySelectorAll(".notification-copy span")].map((item) => item.textContent), ["Second", "Third"]);

  root.querySelector("button")?.click();
  assert.equal(root.children.length, 1);
  center.clear();
  assert.equal(root.children.length, 0);
});
