import assert from "node:assert/strict";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { analyzeCommits } from "@semantic-release/commit-analyzer";
import { runRelease } from "./semantic-release.mjs";

async function outputFile(t) {
  const directory = await mkdtemp(join(tmpdir(), "kwhwise-release-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  return join(directory, "outputs");
}

function outputs(text) {
  return Object.fromEntries(text.trim().split("\n").map((line) => line.split("=")));
}

test("published release provides the exact version and tag for Docker", async (t) => {
  const file = await outputFile(t);
  const result = { nextRelease: { version: "1.2.3", gitTag: "v1.2.3" } };
  const actual = await runRelease({
    env: { GITHUB_OUTPUT: file },
    release: async () => result,
  });
  assert.equal(actual, result);
  assert.deepEqual(outputs(await readFile(file, "utf8")), {
    new_release_published: "true",
    new_release_version: "1.2.3",
    new_release_git_tag: "v1.2.3",
  });
});

test("no relevant commits leaves Docker publication disabled", async (t) => {
  const file = await outputFile(t);
  assert.equal(await runRelease({ env: { GITHUB_OUTPUT: file }, release: async () => false }), false);
  assert.deepEqual(outputs(await readFile(file, "utf8")), { new_release_published: "false" });
});

test("release failure fails the step instead of reporting a published version", async (t) => {
  const file = await outputFile(t);
  const failure = new Error("release publication failed");
  await assert.rejects(runRelease({ env: { GITHUB_OUTPUT: file }, release: async () => { throw failure; } }), failure);
  assert.deepEqual(outputs(await readFile(file, "utf8")), { new_release_published: "false" });
});

test("invalid release metadata cannot select the wrong Docker source tag", async (t) => {
  for (const result of [
    { nextRelease: { version: "1.2.3", gitTag: "v9.9.9" } },
    { nextRelease: { version: "1.2.3\nother_output=true", gitTag: "v1.2.3" } },
    {},
  ]) {
    const file = await outputFile(t);
    await assert.rejects(runRelease({ env: { GITHUB_OUTPUT: file }, release: async () => result }), /invalid stable version/);
    assert.deepEqual(outputs(await readFile(file, "utf8")), { new_release_published: "false" });
  }
});

test("running outside the workflow fails before invoking semantic-release", async () => {
  let called = false;
  await assert.rejects(runRelease({ env: {}, release: async () => { called = true; } }), /GITHUB_OUTPUT is required/);
  assert.equal(called, false);
});

test("Conventional Commits determine the expected release type", async () => {
  const logger = { log() {} };
  for (const [message, expected] of [
    ["feat: add an electricity provider", "minor"],
    ["fix(mqtt): replay prices after discovery", "patch"],
    ["perf: reduce memory use", "patch"],
    ["docs: explain profile updates", null],
    ["ci: update the release workflow", null],
    ["feat!: change the configuration format", "major"],
  ]) {
    const actual = await analyzeCommits({ preset: "conventionalcommits" }, {
      commits: [{ hash: "synthetic", message }], logger, cwd: process.cwd(),
    });
    assert.equal(actual, expected, message);
  }
});
