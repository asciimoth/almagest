#!/usr/bin/env node

// Render the production panel with a local fixture transport. No daemon needed.
import { spawn } from "node:child_process";
import { constants } from "node:fs";
import { access, mkdir, mkdtemp, readFile, rename, rm, writeFile } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { delimiter, extname, isAbsolute, join, resolve, sep } from "node:path";
import { fileURLToPath } from "node:url";
import { parseArgs } from "node:util";
import { setTimeout as delay } from "node:timers/promises";

const root = fileURLToPath(new URL("../../", import.meta.url));
const { values: options } = parseArgs({ options: {
  help: { type: "boolean", short: "h" },
  scenes: { type: "string", default: join(root, "panel/scripts/webdemo-render/scenes.json") },
  only: { type: "string" },
  output: { type: "string", default: join(root, "webdemo/images") },
  size: { type: "string", default: "1500x720" },
  scale: { type: "string", default: "1.5" },
  zoom: { type: "string" },
  padding: { type: "string", default: "40" },
  chromium: { type: "string" },
} });
if (options.help) {
  console.log(`Usage: just render-webdemo-images [options]

Capture the existing Almagest panel in headless Chromium using example graphs
and fixed mock API data. The script does not start a daemon or network nodes.

  --scenes FILE       Scene JSON (default: panel/scripts/webdemo-render/scenes.json)
  --only NAME,...     Render selected scenes (default: all)
  --output DIRECTORY Output directory (default: webdemo/images)
  --size WIDTHxHEIGHT CSS viewport (default: 1500x720)
  --scale NUMBER      Device scale factor (default: 1.5)
  --zoom NUMBER       Override graph scale (default: automatic fit)
  --padding NUMBER    Graph margin in CSS pixels (default: 40)
  --chromium FILE     Browser executable (or CHROMIUM_PATH)
  -h, --help          Show this help

Example paths in scene JSON are relative to the repository. Change positions,
labels, selected nodes, menu data, or snapshot entries to compare variations.
Use --output build/render-variants to preserve the published images.`);
  process.exit(0);
}
const size = /^(\d+)x(\d+)$/u.exec(options.size);
if (!size) throw new Error("--size must use WIDTHxHEIGHT");
const [width, height] = size.slice(1).map(Number);
const scale = Number(options.scale), padding = Number(options.padding);
const zoom = options.zoom === undefined ? undefined : Number(options.zoom);
if (width < 800 || height < 400 || !Number.isFinite(scale) || scale <= 0 || scale > 4
  || !Number.isFinite(padding) || padding < 0 || (zoom !== undefined && (!Number.isFinite(zoom) || zoom <= 0))) {
  throw new Error("Use a viewport of at least 800x400, scale in (0,4], non-negative padding, and positive zoom");
}
const scenes = JSON.parse(await readFile(resolve(options.scenes), "utf8"));
const names = options.only?.split(",") ?? Object.keys(scenes);
if (!names.length || names.some((name) => !/^[a-z][a-z0-9-]*$/u.test(name) || !scenes[name])) {
  throw new Error("Unknown or invalid scene name");
}
const { parseGraphPreviewConfig } = await import("../public/build/graph_preview.js");
const cache = JSON.parse(await readFile(join(root, "webdemo/node-classes.json"), "utf8"));
const locale = JSON.parse(await readFile(join(root, "locales/en/nodes.json"), "utf8"));
const inputs = {};
for (const name of names) {
  const scene = scenes[name];
  inputs[name] = { ...scene, padding, zoom: zoom ?? scene.zoom, cache, locale,
    graph: scene.example ? parseGraphPreviewConfig(await readFile(resolve(root, scene.example), "utf8")) : { nodes: [] } };
}
const chromium = await findChromium(options.chromium);
const output = resolve(options.output);
await mkdir(output, { recursive: true });
const scratch = await mkdtemp(join(tmpdir(), "almagest-webdemo-images-"));
const mime = { ".html": "text/html", ".js": "text/javascript", ".mjs": "text/javascript", ".css": "text/css", ".svg": "image/svg+xml", ".json": "application/json" };
const server = createServer(async (request, response) => {
  try {
    const url = new URL(request.url, "http://127.0.0.1");
    let data, type;
    if (url.pathname === "/") {
      data = (await readFile(join(root, "panel/index.html"), "utf8"))
        .replace("./public/build/admin.js", "/render-client.mjs");
      type = "text/html";
    } else if (url.pathname === "/input") {
      const input = inputs[url.searchParams.get("scene")];
      if (!input) throw new Error("Unknown scene");
      data = JSON.stringify(input); type = "application/json";
    } else {
      const special = { "/render-client.mjs": "scripts/webdemo-render/client.mjs",
        "/logo.svg": "../logo-light.svg", "/favicon.svg": "../logo-light.svg" };
      const path = special[url.pathname] ? resolve(root, "panel", special[url.pathname])
        : resolve(root, "panel", `.${decodeURIComponent(url.pathname)}`);
      if (!special[url.pathname] && !path.startsWith(join(root, "panel") + sep)) throw new Error("Invalid path");
      data = await readFile(path); type = mime[extname(path)] ?? "application/octet-stream";
    }
    response.writeHead(200, { "content-type": type, "cache-control": "no-store" }).end(data);
  } catch (error) {
    response.writeHead(404).end(String(error));
  }
});
let browser, cdp;
try {
  await new Promise((done) => server.listen(0, "127.0.0.1", done));
  const args = ["--headless=new", "--password-store=basic", "--disable-gpu", "--hide-scrollbars",
    "--no-first-run", "--no-default-browser-check", "--disable-background-networking",
    "--remote-debugging-port=0", `--user-data-dir=${join(scratch, "profile")}`, "about:blank"];
  if (process.getuid?.() === 0 || process.env.CI === "true") args.unshift("--no-sandbox");
  browser = spawn(chromium, args, { stdio: ["ignore", "ignore", "pipe"] });
  let browserError = "";
  browser.stderr.on("data", (chunk) => { browserError = (browserError + chunk).slice(-12000); });
  browser.on("error", (error) => { browserError = String(error); });
  const portFile = join(scratch, "profile/DevToolsActivePort");
  let port;
  await until(async () => {
    if (browser.exitCode !== null) throw new Error(`Chromium exited: ${browserError}`);
    try { port = (await readFile(portFile, "utf8")).split("\n")[0]; return !!port; } catch { return false; }
  }, "Chromium startup");
  const pages = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  cdp = await connectCDP(pages.find((page) => page.type === "page").webSocketDebuggerUrl);
  await cdp.send("Page.enable");
  await cdp.send("Runtime.enable");
  await cdp.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor: scale, mobile: false });
  await cdp.send("Emulation.setTimezoneOverride", { timezoneId: "UTC" });
  await cdp.send("Emulation.setLocaleOverride", { locale: "en-US" });
  for (const name of names) {
    await cdp.send("Page.navigate", { url: `http://127.0.0.1:${server.address().port}/?scene=${name}` });
    let report;
    await until(async () => {
      const result = await cdp.send("Runtime.evaluate", { expression: "window.__renderReport", returnByValue: true });
      report = result.result.value;
      if (report?.error) throw new Error(`${name}: ${report.error}`);
      return report?.scene === name && report.ready;
    }, `${name} render`);
    const { data } = await cdp.send("Page.captureScreenshot", { format: "png", captureBeyondViewport: false });
    const png = Buffer.from(data, "base64");
    if (png.readUInt32BE(16) !== Math.round(width * scale) || png.readUInt32BE(20) !== Math.round(height * scale)) {
      throw new Error("Unexpected screenshot dimensions");
    }
    const path = join(output, `${name}.png`);
    await writeFile(`${path}.tmp`, png);
    await rename(`${path}.tmp`, path);
    console.log(`${path} (${report.nodes} nodes, ${report.links} links)`);
  }
} finally {
  cdp?.close();
  if (browser && browser.exitCode === null) {
    const stopped = new Promise((done) => browser.once("close", done));
    browser.kill("SIGTERM");
    const timer = setTimeout(() => browser.kill("SIGKILL"), 3000);
    await stopped; clearTimeout(timer);
  }
  await new Promise((done) => server.close(done));
  await rm(scratch, { recursive: true, force: true });
}

async function until(check, label) {
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    if (await check()) return;
    await delay(100);
  }
  throw new Error(`Timed out: ${label}`);
}

async function findChromium(explicit) {
  const configured = explicit || process.env.CHROMIUM_PATH || process.env.CHROME_PATH || process.env.CHROME_BIN;
  for (const name of configured ? [configured] : ["chromium", "chromium-browser", "google-chrome", "google-chrome-stable"]) {
    const paths = isAbsolute(name) || name.includes(sep) ? [resolve(name)] : (process.env.PATH ?? "").split(delimiter).map((dir) => join(dir, name));
    for (const path of paths) {
      try { await access(path, constants.X_OK); return path; } catch { /* Try the next path. */ }
    }
  }
  throw new Error("Chromium not found. Set --chromium or CHROMIUM_PATH");
}

async function connectCDP(url) {
  const socket = new WebSocket(url);
  await new Promise((done, reject) => { socket.onopen = done; socket.onerror = reject; });
  let id = 0;
  const pending = new Map();
  socket.onmessage = ({ data }) => {
    const message = JSON.parse(data);
    const request = pending.get(message.id);
    if (!request) return;
    pending.delete(message.id); clearTimeout(request.timer);
    if (message.error) request.reject(new Error(message.error.message));
    else request.resolve(message.result);
  };
  socket.onclose = () => {
    for (const request of pending.values()) { clearTimeout(request.timer); request.reject(new Error("Chromium disconnected")); }
    pending.clear();
  };
  return {
    close: () => socket.close(),
    send(method, params = {}) {
      return new Promise((resolveCall, reject) => {
        const requestID = ++id;
        const timer = setTimeout(() => { pending.delete(requestID); reject(new Error(`CDP timeout: ${method}`)); }, 20000);
        pending.set(requestID, { resolve: resolveCall, reject, timer });
        socket.send(JSON.stringify({ id: requestID, method, params }));
      });
    },
  };
}
