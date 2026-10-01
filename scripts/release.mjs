#!/usr/bin/env node
// Builds the print agent and publishes it to the QuickTable API as a
// PRINT_AGENT release — the same flow the tablet's releases use:
//   1. POST /admin/releases/upload-token (validates the release up front)
//   2. upload the .exe straight to Vercel Blob with that one-time token
//      (or, when the API has no Blob store — local dev — as multipart)
//   3. POST /admin/releases, which reads the file back, hashes and signs it.
//
//   npm run release -- --bump patch --notes "..."
//
// Publishing IS the rollout: there are no rollouts for the agent. Every agent
// moves to the newest final release on its own; a beta only reaches the agents
// marked for test builds in the staff console (/staff/print-agents). To pull a
// release back, withdraw it there. See docs/updates.md.
//
// The version lives in package.json. --bump patch|minor|major|prerelease raises
// it before building (none = publish the current one); --version X sets it.
//
// Where a release may come from (see releaseBlocker in releaseVersion.mjs):
// a final version (1.2.0) only from main, clean and identical to origin/main;
// a beta (1.2.0-beta.1) from any branch, clean and pushed.
//
// Order: the version bump is committed and tagged (v1.2.0) locally first, the
// agent is built from that commit, and only once the API accepted it are the
// commit and the tag pushed. If anything fails before that, the local commit
// and tag are removed again.
//
// Options: --api <url> (or QT_API_URL) the API to publish to — also the API the
// agent is built to talk to; --trust-key <keyId:hex> an extra release-signing
// key to compile in (an API with its own key, e.g. a local one); --yes (don't
// ask for confirmation).
// Auth: QT_STAFF_TOKEN, or an interactive staff login — email and password,
// plus the authenticator code for a final release only (a beta gets a session
// that can publish betas and nothing else). The token and password are never printed.

import { execSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import path from "node:path";
import { createInterface } from "node:readline";
import { fileURLToPath } from "node:url";
import { BUMPS, bumpVersion, isPrerelease, parseVersion, releaseBlocker, releaseTag } from "./releaseVersion.mjs";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const CHANNEL = "PRINT_AGENT";
const ARTIFACT_NAME = "quicktable-print-agent.exe";
const UPDATE_PACKAGE = "github.com/quicktable/print-agent/internal/update";
const MULTIPART_THRESHOLD_BYTES = 8 * 1024 * 1024;

function fail(message) {
  console.error(`\n✖ ${message}`);
  process.exit(1);
}

function parseArgs(argv) {
  const options = {};
  for (let i = 0; i < argv.length; i++) {
    const arg = argv[i];
    if (!arg.startsWith("--")) fail(`Unexpected argument "${arg}"`);
    const key = arg.slice(2);
    if (key === "yes") options[key] = true;
    else {
      const value = argv[++i];
      if (value === undefined || value.startsWith("--")) fail(`--${key} needs a value`);
      options[key] = value;
    }
  }
  return options;
}

// --- prompts ---------------------------------------------------------------------

function ask(question) {
  const rl = createInterface({ input: process.stdin, output: process.stderr });
  return new Promise((resolve) =>
    rl.question(question, (answer) => {
      rl.close();
      resolve(answer.trim());
    }),
  );
}

/** Like ask(), but nothing typed is echoed. */
function askHidden(question) {
  return new Promise((resolve, reject) => {
    const stdin = process.stdin;
    if (!stdin.isTTY) return reject(new Error("a password prompt needs a terminal — set QT_STAFF_TOKEN instead"));
    process.stderr.write(question);
    stdin.setRawMode(true);
    stdin.resume();
    stdin.setEncoding("utf8");
    let value = "";
    const onData = (char) => {
      if (char === "\r" || char === "\n" || char === "\u0004") {
        stdin.setRawMode(false);
        stdin.pause();
        stdin.off("data", onData);
        process.stderr.write("\n");
        resolve(value);
      } else if (char === "\u0003") {
        process.stderr.write("\n");
        process.exit(130);
      } else if (char === "\u007f" || char === "\b") {
        value = value.slice(0, -1);
      } else {
        value += char;
      }
    };
    stdin.on("data", onData);
  });
}

// --- API -------------------------------------------------------------------------

async function api(baseUrl, pathname, { method = "GET", body, token } = {}) {
  const isForm = body instanceof FormData;
  let response;
  try {
    response = await fetch(`${baseUrl}${pathname}`, {
      method,
      headers: {
        ...(body !== undefined && !isForm ? { "Content-Type": "application/json" } : {}),
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      body: body === undefined ? undefined : isForm ? body : JSON.stringify(body),
    });
  } catch (err) {
    fail(`Can't reach ${baseUrl} (${err.cause?.code ?? err.message})`);
  }
  const payload = await response.json().catch(() => null);
  if (!response.ok) {
    const retry = response.headers.get("retry-after");
    fail(`${method} ${pathname} → ${response.status}: ${payload?.error ?? response.statusText}${retry ? ` (retry in ${retry}s)` : ""}`);
  }
  return payload?.data;
}

async function staffToken(baseUrl, { beta }) {
  if (process.env.QT_STAFF_TOKEN) return process.env.QT_STAFF_TOKEN;
  console.error(`Staff login on ${baseUrl}`);
  const email = await ask("Email: ");
  const password = await askHidden("Password: ");
  const login = await api(baseUrl, "/auth/login", { method: "POST", body: { email, password } });
  if (login?.role !== "STAFF" || !login.mfa) fail("That account isn't a QuickTable staff account.");
  if (beta) {
    // Enough to publish a beta: it only reaches the agents marked for test builds.
    const session = await api(baseUrl, "/auth/staff/beta-release-token", {
      method: "POST",
      body: { mfaToken: login.mfa.token },
    });
    return session.token;
  }
  if (!login.mfa.enrolled) {
    fail("This staff account has no two-step verification yet — sign in to the staff console once to set it up.");
  }
  const code = await ask("Authenticator code (or a recovery code): ");
  const isRecovery = !/^\d{6}$/.test(code.replace(/\s/g, ""));
  const session = await api(baseUrl, "/auth/staff/mfa/verify", {
    method: "POST",
    body: {
      mfaToken: login.mfa.token,
      ...(isRecovery ? { recoveryCode: code } : { code: code.replace(/\s/g, "") }),
    },
  });
  return session.token;
}

async function publish({ baseUrl, token, version, notes, bytes }) {
  const ticket = await api(baseUrl, "/admin/releases/upload-token", {
    method: "POST",
    token,
    body: { channel: CHANNEL, version, filename: ARTIFACT_NAME },
  });
  if (bytes.length > ticket.maxBytes) {
    fail(`The agent is ${(bytes.length / 1048576).toFixed(1)} MB; ${CHANNEL} allows ${ticket.maxBytes / 1048576} MB.`);
  }

  if (ticket.mode === "direct") {
    console.error("The API has no Blob store (local dev): uploading through the API.");
    const form = new FormData();
    form.append("artifact", new Blob([bytes], { type: "application/octet-stream" }), ARTIFACT_NAME);
    form.append("channel", CHANNEL);
    form.append("version", version);
    if (notes) form.append("releaseNotes", notes);
    return api(baseUrl, "/admin/releases", { method: "POST", token, body: form });
  }

  const { put } = await import("@vercel/blob/client");
  const blob = await put(ticket.pathname, bytes, {
    access: "public",
    token: ticket.token,
    contentType: "application/octet-stream",
    multipart: bytes.length > MULTIPART_THRESHOLD_BYTES,
  }).catch((err) => fail(`Upload to Blob failed: ${err.message}`));

  return api(baseUrl, "/admin/releases", {
    method: "POST",
    token,
    body: { channel: CHANNEL, version, releaseNotes: notes, artifactUrl: blob.url },
  });
}

// --- git ---------------------------------------------------------------------------

const VERSION_FILES = ["package.json", "package-lock.json"];
/** The local release commit/tag, removed again if the release fails before it's published. */
const pending = { commit: false, tag: null, published: false };

/** Rewrites the version in package.json and package-lock.json, keeping their formatting. */
function writeVersion(version) {
  const pkgPath = path.join(ROOT, "package.json");
  writeFileSync(pkgPath, readFileSync(pkgPath, "utf8").replace(/("version"\s*:\s*)"[^"]*"/, `$1"${version}"`));
  const lockPath = path.join(ROOT, "package-lock.json");
  const lockText = readFileSync(lockPath, "utf8");
  const lock = JSON.parse(lockText);
  lock.version = version;
  if (lock.packages?.[""]) lock.packages[""].version = version;
  const eol = lockText.includes("\r\n") ? "\r\n" : "\n";
  writeFileSync(lockPath, JSON.stringify(lock, null, 2).replace(/\n/g, eol) + eol);
}

function run(command, env = process.env) {
  execSync(command, { cwd: ROOT, stdio: ["ignore", "inherit", "inherit"], env });
}

function git(args) {
  return execSync(`git ${args}`, { cwd: ROOT, encoding: "utf8", stdio: ["ignore", "pipe", "pipe"] }).trim();
}

/** Where this working copy stands — the input to releaseBlocker. */
function gitState() {
  try {
    git("fetch --quiet origin");
  } catch {
    fail("Couldn't reach the git remote (git fetch origin) — check your connection, and that the repo has an 'origin'.");
  }
  const branch = git("rev-parse --abbrev-ref HEAD");
  const changes = execSync("git status --porcelain", { cwd: ROOT, encoding: "utf8" }).split("\n").filter(Boolean);
  const onRemote = git("branch -r --contains HEAD") !== "";
  const [behindMain, aheadOfMain] = git("rev-list --left-right --count origin/main...HEAD").split(/\s+/).map(Number);
  return { branch, changes, dirty: changes.length > 0, onRemote, aheadOfMain, behindMain, commit: git("rev-parse --short HEAD") };
}

/** Before building: commit the version bump (if any) and tag the release, locally. */
function prepareRelease(version, current, tag) {
  if (version !== current) {
    writeVersion(version);
    git(`add ${VERSION_FILES.join(" ")}`);
    git(`commit --quiet -m "release: ${version}"`);
    pending.commit = true;
    console.error(`\ngit: committed "release: ${version}".`);
  }
  git(`tag ${tag}`);
  pending.tag = tag;
  console.error(`git: tagged ${tag} @ ${git("rev-parse --short HEAD")} — building from it.`);
}

/** After the API accepted the release: push the release commit and its tag. */
function pushRelease(tag) {
  try {
    git(`push --quiet origin HEAD refs/tags/${tag}`);
    console.error(`  git:       pushed the release commit and ${tag}.`);
  } catch (err) {
    console.error(`\n⚠ The release is published, but pushing it failed: ${String(err.message).split("\n")[0]}`);
    console.error(`  Push it by hand: "git push origin HEAD ${tag}".`);
  }
}

/** A release that failed before being published leaves no trace in git. */
function undoLocalRelease() {
  if (pending.published) return;
  try {
    if (pending.tag) git(`tag -d ${pending.tag}`);
    if (pending.commit) {
      git("reset --soft HEAD~1");
      git(`restore --staged --worktree -- ${VERSION_FILES.join(" ")}`);
    }
    if (pending.tag || pending.commit) console.error("  Undone: the local release commit/tag were removed.");
  } catch {
    console.error(`  Undo by hand: "git tag -d ${pending.tag}"${pending.commit ? ' and "git reset --hard HEAD~1"' : ""}.`);
  }
}

// --- build -------------------------------------------------------------------------

/** Tests, then the Windows executable (no console window) with its version and API baked in. */
function build(version, baseUrl, trustKey) {
  console.error(`\nTesting and building the agent ${version} for ${baseUrl}…`);
  run("go test ./...");
  const flags = [
    "-s",
    "-w",
    "-H",
    "windowsgui",
    `-X main.version=${version}`,
    `-X main.defaultAPIURL=${baseUrl}`,
    ...(trustKey ? [`-X ${UPDATE_PACKAGE}.extraKeys=${trustKey}`] : []),
  ].join(" ");
  const outDir = path.join(ROOT, "dist");
  mkdirSync(outDir, { recursive: true });
  const out = path.join(outDir, ARTIFACT_NAME);
  run(`go build -trimpath -ldflags "${flags}" -o "${out}" ./cmd/agent`, {
    ...process.env,
    GOOS: "windows",
    GOARCH: "amd64",
    CGO_ENABLED: "0",
  });
  return out;
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const baseUrl = (options.api ?? process.env.QT_API_URL ?? "").replace(/\/$/, "");
  if (!/^https?:\/\//.test(baseUrl)) fail("Say which API to publish to: --api https://… or QT_API_URL.");
  if (options.bump && !BUMPS.includes(options.bump)) fail(`--bump must be one of ${BUMPS.join(", ")}`);
  if (options.bump && options.version) fail("Use --bump or --version, not both.");
  const trustKey = options["trust-key"] ?? process.env.QT_TRUST_KEY ?? "";
  if (trustKey && !/^[\w.-]+:[0-9a-f]{64}$/.test(trustKey)) fail("--trust-key must be <keyId>:<64 hex characters>");

  const current = JSON.parse(readFileSync(path.join(ROOT, "package.json"), "utf8")).version;
  const version = options.version ?? bumpVersion(current, options.bump ?? "none");
  if (!version || !parseVersion(version)) {
    fail(`"${version ?? current}" isn't MAJOR.MINOR.PATCH or MAJOR.MINOR.PATCH-beta.N — fix "version" in package.json.`);
  }
  const final = !isPrerelease(version);

  const state = gitState();
  const blocker = releaseBlocker({ version, ...state });
  if (blocker) {
    if (state.dirty) console.error(state.changes.slice(0, 8).map((c) => `    ${c}`).join("\n"));
    fail(blocker);
  }
  const tag = releaseTag(version);
  if (git(`tag --list ${tag}`) !== "" || git(`ls-remote --tags origin refs/tags/${tag}`) !== "") {
    fail(`${tag} already exists: ${version} was already released. Pick another version.`);
  }

  console.error(`\n  API:      ${baseUrl}`);
  console.error(`  Channel:  ${CHANNEL}`);
  console.error(`  Version:  ${version}${final ? "" : " (beta)"}${version !== current ? ` — package.json goes ${current} → ${version}` : ""}`);
  console.error(`  Code:     ${state.branch} @ ${state.commit}`);
  if (trustKey) console.error(`  Trusts:   the production key + ${trustKey.split(":")[0]}`);
  if (options.notes) console.error(`  Notes:    ${options.notes}`);
  console.error(`  Git:      ${version !== current ? "commit the version bump, " : ""}tag ${tag}, build from it; push once published`);
  console.error(
    final
      ? "  Reaches:  EVERY print agent, on its own (each restaurant's update window, or when the agent restarts)."
      : "  Reaches:  only the agents marked for test builds in the staff console.",
  );
  if (!options.yes && !/^(y|yes)$/i.test(await ask("\nPublish? [y/N] "))) fail("Cancelled.");

  prepareRelease(version, current, tag);
  const file = build(version, baseUrl, trustKey);
  const bytes = readFileSync(file);
  console.error(`Built ${path.relative(ROOT, file)} (${(bytes.length / 1048576).toFixed(2)} MB)`);

  const token = await staffToken(baseUrl, { beta: !final });
  const release = await publish({ baseUrl, token, version, notes: options.notes, bytes });
  pending.published = true;
  console.error(`\n✔ Published ${release.channel} ${release.version}`);
  console.error(`  id:        ${release.id}`);
  console.error(`  sha256:    ${release.artifactSha256}`);
  console.error(`  signed by: ${(release.signatures ?? []).map((s) => s.keyId).join(", ") || "—"}`);
  pushRelease(tag);
  console.error("  Follow it in the staff console: /staff/print-agents (who updated, who failed; withdraw it there).");
}

main().catch((err) => fail(err.message));
process.on("exit", (code) => {
  // Something failed after the local commit/tag (tests, build, login, upload…).
  if (code !== 0) undoLocalRelease();
});
