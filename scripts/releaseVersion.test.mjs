import assert from "node:assert/strict";
import { test } from "node:test";
import { bumpVersion, isPrerelease, releaseBlocker, releaseTag } from "./releaseVersion.mjs";

test("bumps like npm does, betas included", () => {
  assert.equal(bumpVersion("1.4.2", "patch"), "1.4.3");
  assert.equal(bumpVersion("1.4.2", "minor"), "1.5.0");
  assert.equal(bumpVersion("1.4.2", "major"), "2.0.0");
  assert.equal(bumpVersion("1.4.2", "prerelease"), "1.4.3-beta.1");
  assert.equal(bumpVersion("1.4.3-beta.1", "prerelease"), "1.4.3-beta.2");
  assert.equal(bumpVersion("1.4.3-beta.2", "patch"), "1.4.3");
  assert.equal(bumpVersion("1.4.2", "none"), "1.4.2");
  assert.equal(bumpVersion("dev", "patch"), null);
});

test("tags a release by its version", () => {
  assert.equal(releaseTag("1.4.3-beta.1"), "v1.4.3-beta.1");
  assert.equal(isPrerelease("1.4.3-beta.1"), true);
  assert.equal(isPrerelease("1.4.3"), false);
});

test("a final release only comes from an up-to-date main", () => {
  const clean = { dirty: false, onRemote: true, aheadOfMain: 0, behindMain: 0 };
  assert.equal(releaseBlocker({ version: "1.0.0", branch: "main", ...clean }), null);
  assert.match(releaseBlocker({ version: "1.0.0", branch: "feature/x", ...clean }), /only comes from main/);
  assert.match(releaseBlocker({ version: "1.0.0", branch: "main", ...clean, aheadOfMain: 1 }), /push first/);
  assert.match(releaseBlocker({ version: "1.0.0", branch: "main", ...clean, dirty: true }), /uncommitted/);
});

test("a beta comes from any pushed branch", () => {
  const state = { branch: "feature/x", dirty: false, aheadOfMain: 3, behindMain: 0 };
  assert.equal(releaseBlocker({ version: "1.0.1-beta.1", ...state, onRemote: true }), null);
  assert.match(releaseBlocker({ version: "1.0.1-beta.1", ...state, onRemote: false }), /isn't pushed/);
});
