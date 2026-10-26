#!/usr/bin/env node

import { spawn } from "node:child_process";
import { constants } from "node:fs";
import { access, mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { createServer } from "node:http";
import { tmpdir } from "node:os";
import { basename, delimiter, dirname, isAbsolute, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const panelDirectory = fileURLToPath(new URL("../", import.meta.url));
const repositoryDirectory = fileURLToPath(new URL("../../", import.meta.url));
const defaults = { width: 640, height: 320, padding: 32 };

if (process.argv.includes("--help") || process.argv.includes("-h")) {
  process.stdout.write(`Usage: npm run graph:image -- [options] [graph-file]\n\nRender an almagestd graph description to a PNG image. Read stdin when no graph\nfile or --graph-json value is specified.\n\nOptions:\n  -o, --output FILE         Output PNG path (default: graph.png)\n      --graph FILE          Read the graph from FILE\n      --graph-json TEXT     Use TEXT as the graph description\n      --size WIDTHxHEIGHT   Canvas size (default: 640x320)\n      --width PIXELS        Canvas width\n      --height PIXELS       Canvas height\n      --zoom SCALE          Fixed graph scale; default is automatic fitting\n      --padding PIXELS      Padding on every side (default: 32)\n      --padding-top PIXELS  Override the top padding\n      --padding-right PIXELS\n      --padding-bottom PIXELS\n      --padding-left PIXELS\n      --node-classes FILE   Node class cache (default: ../webdemo/node-classes.json)\n      --chromium FILE       Chromium-compatible executable (or CHROMIUM_PATH)\n  -h, --help                Show this help\n`);
  process.exit(0);
}

const arguments_ = parseArguments(process.argv.slice(2));
const source = arguments_.graphJSON ?? await readGraphSource(arguments_.graphFile);
const nodeClasses = await readFile(arguments_.nodeClasses, "utf8");
validateJSON(nodeClasses, "Node class cache");
const chromium = await findChromium(arguments_.chromium);
const output = resolve(arguments_.output);
await mkdir(dirname(output), { recursive: true });

const temporaryDirectory = await mkdtemp(join(tmpdir(), "almagest-graph-image-"));
let completion;
const server = createServer(async (request, response) => {
  try {
    const url = new URL(request.url ?? "/", "http://127.0.0.1");
    if (request.method === "POST" && url.pathname === "/done") {
      completion = url.searchParams.get("error") || true;
      response.writeHead(204).end();
      return;
    }
    if (url.pathname === "/") return send(response, html(), "text/html; charset=utf-8");
    if (url.pathname === "/input") return send(response, JSON.stringify({ source, options: arguments_.options }), "application/json");
    if (url.pathname === "/node-classes") return send(response, nodeClasses, "application/json");
    if (url.pathname === "/vendor/litegraph.js") {
      return send(response, await readFile(join(panelDirectory, "vendor/litegraph/litegraph.js")), "text/javascript");
    }
    if (url.pathname.startsWith("/build/")) {
      const name = basename(url.pathname);
      if (!/^[a-z0-9_]+\.js$/u.test(name)) throw new Error("Invalid module path");
      return send(response, await readFile(join(panelDirectory, "public/build", name)), "text/javascript");
    }
    response.writeHead(404).end("Not found");
  } catch (error) {
    response.writeHead(500, { "content-type": "text/plain; charset=utf-8" }).end(String(error));
  }
});

try {
  await new Promise((resolveListen, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolveListen);
  });
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("Could not start the graph renderer server");
  const browserArguments = [
    "--headless=new",
    "--password-store=basic",
    "--disable-gpu",
    "--hide-scrollbars",
    "--no-first-run",
    "--no-default-browser-check",
    "--force-device-scale-factor=1",
    "--run-all-compositor-stages-before-draw",
    "--virtual-time-budget=10000",
    `--user-data-dir=${join(temporaryDirectory, "profile")}`,
    `--window-size=${arguments_.options.width},${arguments_.options.height}`,
    `--screenshot=${output}`,
    `http://127.0.0.1:${address.port}/`,
  ];
  if (typeof process.getuid === "function" && process.getuid() === 0) browserArguments.unshift("--no-sandbox");
  await rm(output, { force: true });
  await runBrowser(chromium, browserArguments);
  if (completion !== true) {
    if (typeof completion === "string") throw new Error(completion);
    throw new Error("Chromium did not complete the graph render");
  }
  const image = await readFile(output);
  const pngSignature = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]);
  if (image.length < 24 || !image.subarray(0, 8).equals(pngSignature)) {
    throw new Error("Chromium did not create a PNG image");
  }
  if (image.readUInt32BE(16) !== arguments_.options.width || image.readUInt32BE(20) !== arguments_.options.height) {
    throw new Error("Chromium created an image with an unexpected size");
  }
  process.stdout.write(`${output}\n`);
} finally {
  await new Promise((resolveClose) => server.close(resolveClose));
  await rm(temporaryDirectory, { recursive: true, force: true });
}

function parseArguments(values) {
  const result = {
    output: "graph.png",
    nodeClasses: join(repositoryDirectory, "webdemo/node-classes.json"),
    options: { width: defaults.width, height: defaults.height, padding: {} },
  };
  let generalPadding = defaults.padding;
  const individualPadding = {};
  for (let index = 0; index < values.length; index += 1) {
    const option = values[index];
    if (!option?.startsWith("-")) {
      if (result.graphFile) fail("Specify only one graph file");
      result.graphFile = resolve(option);
      continue;
    }
    const value = () => {
      const next = values[index + 1];
      if (next === undefined) fail(`${option} requires a value`);
      index += 1;
      return next;
    };
    switch (option) {
      case "-o": case "--output": result.output = value(); break;
      case "--graph": result.graphFile = resolve(value()); break;
      case "--graph-json": result.graphJSON = value(); break;
      case "--size": {
        const match = /^(\d+)[x×](\d+)$/u.exec(value());
        if (!match) fail("--size must use WIDTHxHEIGHT");
        result.options.width = positiveInteger(match[1], "--size width");
        result.options.height = positiveInteger(match[2], "--size height");
        break;
      }
      case "--width": result.options.width = positiveInteger(value(), option); break;
      case "--height": result.options.height = positiveInteger(value(), option); break;
      case "--zoom": result.options.zoom = positiveNumber(value(), option); break;
      case "--padding": generalPadding = nonNegativeNumber(value(), option); break;
      case "--padding-top": individualPadding.top = nonNegativeNumber(value(), option); break;
      case "--padding-right": individualPadding.right = nonNegativeNumber(value(), option); break;
      case "--padding-bottom": individualPadding.bottom = nonNegativeNumber(value(), option); break;
      case "--padding-left": individualPadding.left = nonNegativeNumber(value(), option); break;
      case "--node-classes": result.nodeClasses = resolve(value()); break;
      case "--chromium": result.chromium = resolve(value()); break;
      default: fail(`Unknown option: ${option}`);
    }
  }
  if (result.graphFile && result.graphJSON !== undefined) fail("Use only one graph input");
  result.options.padding = {
    top: individualPadding.top ?? generalPadding,
    right: individualPadding.right ?? generalPadding,
    bottom: individualPadding.bottom ?? generalPadding,
    left: individualPadding.left ?? generalPadding,
  };
  return result;
}

async function readGraphSource(path) {
  if (path) return await readFile(path, "utf8");
  if (process.stdin.isTTY) fail("Specify a graph file, --graph-json, or stdin");
  const chunks = [];
  for await (const chunk of process.stdin) chunks.push(chunk);
  return Buffer.concat(chunks).toString("utf8");
}

async function findChromium(explicit) {
  const configured = explicit || process.env.CHROMIUM_PATH || process.env.CHROME_PATH || process.env.CHROME_BIN;
  const names = configured ? [configured] : ["chromium", "chromium-browser", "google-chrome", "google-chrome-stable"];
  for (const name of names) {
    const candidates = isAbsolute(name) ? [name] : (process.env.PATH ?? "").split(delimiter).map((directory) => join(directory, name));
    for (const candidate of candidates) {
      try {
        await access(candidate, constants.X_OK);
        return candidate;
      } catch { /* Try the next executable. */ }
    }
  }
  fail("Could not find Chromium. Use --chromium or CHROMIUM_PATH");
}

function runBrowser(executable, arguments_) {
  return new Promise((resolveRun, reject) => {
    const child = spawn(executable, arguments_, { stdio: ["ignore", "ignore", "pipe"] });
    let errors = "";
    const timeout = setTimeout(() => child.kill("SIGKILL"), 30_000);
    child.stderr.setEncoding("utf8");
    child.stderr.on("data", (value) => { errors += value; });
    child.once("error", reject);
    child.once("close", (code, signal) => {
      clearTimeout(timeout);
      if (code === 0) resolveRun();
      else reject(new Error(`Chromium failed (${signal ?? code}): ${errors.trim()}`));
    });
  });
}

function html() {
  return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'self'; connect-src 'self'; style-src 'unsafe-inline'"><style>html,body,#graph{width:100%;height:100%;margin:0;overflow:hidden;background:#11111b}canvas{display:block}</style><script src="/vendor/litegraph.js"></script><script type="module" src="/build/graph_image.js"></script></head><body><main id="graph"></main></body></html>`;
}

function send(response, content, contentType) {
  response.writeHead(200, { "content-type": contentType, "cache-control": "no-store", "x-content-type-options": "nosniff" });
  response.end(content);
}

function validateJSON(source, name) {
  try { JSON.parse(source); } catch (error) { fail(`${name} is not valid JSON: ${error.message}`); }
}

function positiveInteger(value, name) {
  const number = positiveNumber(value, name);
  if (!Number.isInteger(number)) fail(`${name} must be an integer`);
  return number;
}

function positiveNumber(value, name) {
  const number = Number(value);
  if (!Number.isFinite(number) || number <= 0) fail(`${name} must be a positive number`);
  return number;
}

function nonNegativeNumber(value, name) {
  const number = Number(value);
  if (!Number.isFinite(number) || number < 0) fail(`${name} must be a non-negative number`);
  return number;
}

function fail(message) {
  throw new Error(message);
}
