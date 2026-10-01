// Version rules for scripts/release.mjs (kept apart so they can be unit tested).
//
// A final release is MAJOR.MINOR.PATCH; a test build is MAJOR.MINOR.PATCH-beta.N,
// which sorts before its final release (1.1.1-beta.2 < 1.1.1) wherever the API
// compares versions.

export const BUMPS = ["patch", "minor", "major", "prerelease", "none"];

const VERSION = /^(\d+)\.(\d+)\.(\d+)(?:-beta\.(\d+))?$/;

export function parseVersion(version) {
  const match = VERSION.exec(version);
  if (!match) return null;
  const [, major, minor, patch, beta] = match;
  return { major: +major, minor: +minor, patch: +patch, beta: beta === undefined ? null : +beta };
}

export function isPrerelease(version) {
  return /-/.test(version);
}

/**
 * The next version, with npm's semantics for pre-releases:
 * - patch/minor/major from a final release bump that part (1.4.2 → 1.4.3 / 1.5.0 / 2.0.0);
 *   from a beta they finish it when it's already that kind of bump (1.4.3-beta.2 → 1.4.3
 *   with patch; 1.5.0-beta.1 → 1.5.0 with minor), otherwise bump as usual.
 * - prerelease: a beta of the next patch (1.4.2 → 1.4.3-beta.1), or the next beta
 *   (1.4.3-beta.1 → 1.4.3-beta.2).
 * - none: unchanged.
 * Returns null for a version outside MAJOR.MINOR.PATCH[-beta.N].
 */
export function bumpVersion(current, bump) {
  const v = parseVersion(current);
  if (!v) return null;
  const { major, minor, patch, beta } = v;
  const pre = beta !== null;
  switch (bump) {
    case "none":
      return current;
    case "prerelease":
      return pre ? `${major}.${minor}.${patch}-beta.${beta + 1}` : `${major}.${minor}.${patch + 1}-beta.1`;
    case "patch":
      return pre ? `${major}.${minor}.${patch}` : `${major}.${minor}.${patch + 1}`;
    case "minor":
      return pre && patch === 0 ? `${major}.${minor}.0` : `${major}.${minor + 1}.0`;
    case "major":
      return pre && minor === 0 && patch === 0 ? `${major}.0.0` : `${major + 1}.0.0`;
    default:
      return null;
  }
}

/** The git tag a published release gets: v0.1.1, v1.1.1-beta.2. */
export function releaseTag(version) {
  return `v${version}`;
}

/**
 * Why the working copy can't publish this version, or null when it can.
 * A final release only from main, identical to origin/main (everything merged and
 * pushed, nothing to pull); a beta from any branch, as long as it's pushed. Both
 * need a clean working tree, so the artifact matches a commit exactly.
 */
export function releaseBlocker({ version, branch, dirty, onRemote, aheadOfMain, behindMain }) {
  if (dirty) return "There are uncommitted changes: commit (and push) them first, so the release matches a commit.";
  if (isPrerelease(version)) {
    if (!onRemote) return `This commit isn't pushed: push '${branch}' first, so the beta can be traced back to it.`;
    return null;
  }
  if (branch !== "main") {
    return `A final release (${version}) only comes from main — merge '${branch}' into main first, or publish a beta (-beta.N) from this branch.`;
  }
  if (aheadOfMain > 0) return `main has ${aheadOfMain} commit(s) not pushed to origin: push first.`;
  if (behindMain > 0) return `origin/main has ${behindMain} commit(s) you don't have: pull first.`;
  return null;
}
