import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { test } from "node:test";
import { JSDOM } from "jsdom";
import {
  createClassButtonContent,
  createEmptyWorkerItem,
  createLogItem,
  createNodeNameForm,
  createNotificationToast,
  createWorkerNodeBlock,
  renderNodeDescription,
  updateWorkerAgeElements,
} from "../public/build/safe_dom.js";

const payload = `<img src=x onerror="globalThis.__xss = true"><script>globalThis.__xss = true</script>`;

function installDOM() {
  const dom = new JSDOM("<!doctype html><main></main>", {
    url: "https://panel.test/",
  });
  globalThis.window = dom.window;
  globalThis.document = dom.window.document;
  return dom;
}

function assertNoParsedPayload(root) {
  assert.equal(root.querySelector("script,img,svg,iframe,math"), null);
  assert.equal(globalThis.__xss, undefined);
}

test("node-sourced class picker text is not parsed as HTML", () => {
  installDOM();
  const root = document.createElement("button");
  root.append(...createClassButtonContent({
    class: `evil/${payload}`,
    short_description: payload,
    long_description: "",
    primary_type: "",
    initial_ports: [],
  }, [
    { name: payload, color: "#f00" },
  ], [
    { name: payload, color: "#0f0" },
  ]));

  assertNoParsedPayload(root);
  assert.match(root.textContent ?? "", /<img src=x/);
});

test("node name, logs, and notifications keep payloads as text", () => {
  installDOM();
  const root = document.createElement("div");
  root.append(
    createNodeNameForm(payload),
    createLogItem({
      at: payload,
      source: "node",
      level: "error",
      message: payload,
      node_id: 7,
      node_class: `evil/${payload}`,
    }),
    createNotificationToast(payload),
  );

  assertNoParsedPayload(root);
  assert.equal(root.querySelector("input")?.value, payload);
  assert.match(root.textContent ?? "", /<script>/);
});

test("worker process blocks keep node and worker payloads as text", () => {
  installDOM();
  const root = document.createElement("ol");
  root.append(
    createWorkerNodeBlock({
      nodeID: 4,
      node: {
        class: `evil/${payload}`,
        name: payload,
        primary_type: "demo/type",
        label: "",
        position: "",
        placeholder: false,
        root: false,
        has_root_path: false,
        left_ports: [],
        right_ports: [],
      },
      fallbackClass: `evil/${payload}`,
      fallbackName: payload,
      workers: [
        {
          id: 9,
          node: 4,
          class: `evil/${payload}`,
          name: payload,
          worker_name: payload,
          started_at: new Date(Date.now() - 2000).toISOString(),
          orphan: false,
          error: payload,
        },
      ],
      color: "#89b4fa",
      textColor: "#ffffff",
    }, new Date()),
    createEmptyWorkerItem(),
  );

  assertNoParsedPayload(root);
  assert.match(root.textContent ?? "", /worker 9/);
  assert.match(root.textContent ?? "", /<script>/);
});

test("worker age updates preserve existing worker rows", () => {
  installDOM();
  const root = document.createElement("ol");
  const started = new Date("2026-01-01T00:00:00.000Z").toISOString();
  root.append(
    createWorkerNodeBlock({
      nodeID: 4,
      node: undefined,
      fallbackClass: "demo/Worker",
      fallbackName: "worker-node",
      workers: [
        {
          id: 9,
          node: 4,
          class: "demo/Worker",
          name: "worker-node",
          worker_name: "runner",
          started_at: started,
          orphan: false,
        },
      ],
    }, new Date("2026-01-01T00:00:02.000Z")),
  );

  const row = root.querySelector(".ps-worker");
  const age = root.querySelector(".ps-worker-age");
  assert.equal(age?.textContent, "age 2s");

  updateWorkerAgeElements(root, new Date("2026-01-01T00:01:05.000Z"));

  assert.equal(root.querySelector(".ps-worker"), row);
  assert.equal(root.querySelector(".ps-worker-age"), age);
  assert.equal(age?.textContent, "age 1m5s");
});


test("node descriptions allow safe markdown only", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, `[${payload}](javascript:alert(1)) **${payload}** \`${payload}\``);

  assertNoParsedPayload(root);
  assert.equal(root.querySelector("a")?.hasAttribute("href"), false);
  assert.match(root.textContent ?? "", /<img src=x/);
});

test("node descriptions render multiline code blocks as text", () => {
  installDOM();
  const root = document.createElement("div");
  renderNodeDescription(root, [
    "Before",
    "",
    "```ts",
    "const value = `<script>globalThis.__xss = true</script>`;",
    "",
    "console.log(value);",
    "```",
    "",
    "After `inline`",
  ].join("\n"));

  assertNoParsedPayload(root);
  const blocks = [...root.children].map((child) => child.tagName.toLowerCase());
  assert.deepEqual(blocks, ["p", "pre", "p"]);

  const code = root.querySelector("pre > code");
  assert.equal(code?.className, "language-ts");
  assert.equal(code?.textContent, [
    "const value = `<script>globalThis.__xss = true</script>`;",
    "",
    "console.log(value);",
  ].join("\n"));
  assert.equal(root.querySelectorAll("p code").length, 1);
});

test("panel source does not use HTML parser APIs for rendering", async () => {
  const files = ["src/app.ts", "src/safe_dom.ts"];
  for (const file of files) {
    const source = await readFile(new URL(`../${file}`, import.meta.url), "utf8");
    assert.doesNotMatch(source, /\b(?:innerHTML|outerHTML|insertAdjacentHTML)\b/, file);
  }
});
