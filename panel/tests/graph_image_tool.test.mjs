import assert from "node:assert/strict";
import { access, mkdtemp, readFile, rm } from "node:fs/promises";
import { execFile } from "node:child_process";
import { delimiter, join } from "node:path";
import { tmpdir } from "node:os";
import { promisify } from "node:util";
import { test } from "node:test";

const execute = promisify(execFile);
const script = new URL("../scripts/render-graph-image.mjs", import.meta.url);
const repository = new URL("../../", import.meta.url);

test("graph image tool documents graph, sizing, zoom, and padding inputs", async () => {
  const { stdout } = await execute(process.execPath, [script.pathname, "--help"]);
  assert.match(stdout, /graph-file/);
  assert.match(stdout, /--size WIDTHxHEIGHT/);
  assert.match(stdout, /--zoom SCALE/);
  assert.match(stdout, /--padding-top PIXELS/);
  assert.match(stdout, /CHROMIUM_PATH/);
});

test("graph image Chromium process does not access the system secret store", async () => {
  const source = await readFile(script, "utf8");
  assert.match(source, /"--password-store=basic"/);
});

test("repository just command builds and invokes the graph image tool", async () => {
  const justfile = await readFile(new URL("justfile", repository), "utf8");
  assert.match(justfile, /render-graph-image \*args:[\s\S]*npm --prefix panel run compile/);
  assert.match(justfile, /node panel\/scripts\/render-graph-image\.mjs \{\{args\}\}/);
});

test("graph image tool rejects invalid dimensions before it starts Chromium", async () => {
  await assert.rejects(
    execute(process.execPath, [script.pathname, "--size", "wide", "--graph-json", "{}"]),
    /--size must use WIDTHxHEIGHT/,
  );
});

test("graph image tool renders an exact-size PNG with ephemeral Chromium", { timeout: 40_000 }, async (context) => {
  const chromium = await findChromium();
  if (!chromium) return context.skip("Chromium is not installed");
  const directory = await mkdtemp(join(tmpdir(), "almagest-graph-image-test-"));
  const output = join(directory, "graph.png");
  try {
    const graph = JSON.stringify({
      Client: {
        Class: "almagest/HttpClient",
        Pos: "{\"x\":100,\"y\":100}",
        Label: "ready <script>",
        Links: ["Network -> [Internet] Network"],
      },
      Internet: { Class: "almagest/OutNetwork", Pos: "{\"x\":380,\"y\":100}" },
    });
    const { stdout } = await execute(process.execPath, [
      script.pathname,
      "--chromium", chromium,
      "--graph-json", graph,
      "--output", output,
      "--size", "401x223",
      "--padding", "20",
      "--padding-left", "45",
    ], { cwd: new URL("../", import.meta.url), timeout: 35_000 });
    assert.equal(stdout.trim(), output);
    const png = await readFile(output);
    assert.deepEqual([...png.subarray(0, 8)], [137, 80, 78, 71, 13, 10, 26, 10]);
    assert.equal(png.readUInt32BE(16), 401);
    assert.equal(png.readUInt32BE(20), 223);
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

async function findChromium() {
  for (const name of ["chromium", "chromium-browser", "google-chrome", "google-chrome-stable"]) {
    for (const directory of (process.env.PATH ?? "").split(delimiter)) {
      const candidate = join(directory, name);
      try {
        await access(candidate);
        return candidate;
      } catch { /* Try the next path. */ }
    }
  }
  return null;
}
