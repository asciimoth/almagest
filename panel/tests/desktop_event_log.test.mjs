import assert from "node:assert/strict";
import { test } from "node:test";

test("native logging is inactive until the desktop entry configures it", async () => {
  const { configureDesktopEventLogger, logDesktopEvent } = await import(
    "../public/build/desktop_event_log.js?scope-test"
  );
  assert.doesNotThrow(() => logDesktopEvent("docs_frame_created"));
  const events = [];
  configureDesktopEventLogger((event) => events.push(event));
  logDesktopEvent("docs_frame_ready");
  assert.deepEqual(events, ["docs_frame_ready"]);
});
