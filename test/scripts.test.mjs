import assert from "node:assert/strict";
import { chmod, cp, mkdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import { mkdtempSync } from "node:fs";
import os from "node:os";
import path from "node:path";
import { afterEach, test } from "node:test";

const repoRoot = path.resolve(import.meta.dirname, "..");
const temporaryDirectories = [];
afterEach(async () => Promise.all(temporaryDirectories.splice(0).map((directory) => rm(directory, { recursive: true, force: true }))));

function temporaryDirectory() {
  const directory = mkdtempSync(path.join(os.tmpdir(), "gripi-scripts-"));
  temporaryDirectories.push(directory);
  return directory;
}

async function executable(file, contents) {
  await mkdir(path.dirname(file), { recursive: true });
  await writeFile(file, contents);
  await chmod(file, 0o755);
}

function run(command, args = [], options = {}) {
  return spawnSync(command, args, { encoding: "utf8", ...options });
}

async function launcherFixture() {
  const root = temporaryDirectory();
  const project = path.join(root, "project");
  const fakeBin = path.join(root, "fake-bin");
  const launcher = path.join(project, "bin", "start");
  const gateway = path.join(project, "tmp", "gripi");
  const calls = path.join(root, "calls");
  const restart = path.join(root, "state", "restart-request");
  await mkdir(fakeBin, { recursive: true });
  await mkdir(path.dirname(launcher), { recursive: true });
  await cp(path.join(repoRoot, "bin/start"), launcher);
  await chmod(launcher, 0o755);
  return {
    root, project, fakeBin, launcher, gateway, calls, restart,
    env: { ...process.env, PATH: `${fakeBin}:${process.env.PATH}`, CALLS_PATH: calls, RESTART_PATH: restart, GRIPI_RESTART_PATH: restart, GRIPI_HOST: "", GRIPI_PORT: "" },
  };
}

test("setup installs Node dependencies, builds Go, and delegates password creation", async () => {
  const root = temporaryDirectory();
  const project = path.join(root, "project");
  const fakeBin = path.join(root, "fake-bin");
  const calls = path.join(root, "mise-calls");
  const envPath = path.join(root, "gripi-env");
  await mkdir(path.join(project, "bin"), { recursive: true });
  await mkdir(fakeBin, { recursive: true });
  await cp(path.join(repoRoot, "bin/setup"), path.join(project, "bin/setup"));
  await cp(path.join(repoRoot, "bin/gripi-password"), path.join(project, "bin/gripi-password"));
  await executable(path.join(fakeBin, "mise"), `#!/bin/sh
printf '%s\n' "$*" >> "$MISE_CALLS"
if [ "$*" = "exec -- go build -o tmp/gripi ./cmd/gripi" ]; then
  mkdir -p tmp
  cat > tmp/gripi <<'EOF'
#!/bin/sh
[ "$1" = password ] || exit 2
printf 'GRIPI_ADMIN_PASSWORD=0123456789abcdef01234567\n' > "$GRIPI_ENV_PATH"
EOF
  chmod +x tmp/gripi
fi
`);
  const result = run(path.join(project, "bin/setup"), [], { cwd: project, env: { ...process.env, PATH: `${fakeBin}:${process.env.PATH}`, MISE_CALLS: calls, GRIPI_ENV_PATH: envPath } });
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual((await readFile(calls, "utf8")).trim().split("\n"), ["exec -- npm ci", "exec -- go build -o tmp/gripi ./cmd/gripi"]);
  assert.equal(await readFile(envPath, "utf8"), "GRIPI_ADMIN_PASSWORD=0123456789abcdef01234567\n");
});

test("password wrapper requires and delegates to the built Go gateway", async () => {
  const root = temporaryDirectory();
  const bin = path.join(root, "bin");
  const wrapper = path.join(bin, "gripi-password");
  await mkdir(bin, { recursive: true });
  await cp(path.join(repoRoot, "bin/gripi-password"), wrapper);
  await chmod(wrapper, 0o755);

  let result = run(wrapper);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /Built Go gateway not found/);

  await executable(path.join(root, "tmp/gripi"), "#!/bin/sh\nprintf '%s' \"$1\"\n");
  result = run(wrapper);
  assert.equal(result.status, 0, result.stderr);
  assert.equal(result.stdout, "password");
});

test("launcher clears stale restart state, passes production defaults, and preserves exit status", async () => {
  const fixture = await launcherFixture();
  await executable(fixture.gateway, "#!/bin/sh\nprintf '%s|%s|%s\n' \"$*\" \"$APP_ENV\" \"$GRIPI_BIND_HOST\" >> \"$CALLS_PATH\"\nexit 23\n");
  await mkdir(path.dirname(fixture.restart), { recursive: true });
  await writeFile(fixture.restart, "stale");
  const result = run(fixture.launcher, [], { cwd: fixture.project, env: fixture.env });
  assert.equal(result.status, 23);
  assert.equal(await readFile(fixture.calls, "utf8"), "|production|127.0.0.1\n");
  await assert.rejects(readFile(fixture.restart));

  const hostCalls = path.join(fixture.root, "host-calls");
  const hostResult = run(fixture.launcher, ["100.64.0.1"], { cwd: fixture.project, env: { ...fixture.env, CALLS_PATH: hostCalls } });
  assert.equal(hostResult.status, 23);
  assert.equal(await readFile(hostCalls, "utf8"), "|production|100.64.0.1\n");
});

test("launcher consumes restart requests and reloads exactly once", async () => {
  const fixture = await launcherFixture();
  await executable(fixture.gateway, `#!/bin/sh
count=$(wc -l < "$CALLS_PATH" 2>/dev/null || printf 0)
printf 'run\n' >> "$CALLS_PATH"
if [ "$count" -eq 0 ]; then mkdir -p "$(dirname "$RESTART_PATH")"; touch "$RESTART_PATH"; exit 17; fi
exit 29
`);
  const result = run(fixture.launcher, [], { cwd: fixture.project, env: fixture.env });
  assert.equal(result.status, 29, result.stderr);
  assert.deepEqual((await readFile(fixture.calls, "utf8")).trim().split("\n"), ["run", "run"]);
});

test("launcher atomically bootstraps a missing Go binary only once across restart", async () => {
  const fixture = await launcherFixture();
  const buildCalls = path.join(fixture.root, "build-calls");
  await executable(path.join(fixture.fakeBin, "mise"), `#!/bin/sh
printf '%s\n' "$*" >> "$BUILD_CALLS"
previous=''
for argument in "$@"; do
  if [ "$previous" = "-o" ]; then output="$argument"; break; fi
  previous="$argument"
done
mkdir -p "$(dirname "$output")"
cat > "$output" <<'EOF'
#!/bin/sh
count=$(wc -l < "$CALLS_PATH" 2>/dev/null || printf 0)
printf 'run\n' >> "$CALLS_PATH"
if [ "$count" -eq 0 ]; then mkdir -p "$(dirname "$RESTART_PATH")"; touch "$RESTART_PATH"; fi
EOF
chmod +x "$output"
`);
  const result = run(fixture.launcher, [], { cwd: fixture.project, env: { ...fixture.env, BUILD_CALLS: buildCalls } });
  assert.equal(result.status, 0, result.stderr);
  assert.equal((await readFile(buildCalls, "utf8")).trim().split("\n").length, 1);
  assert.match(await readFile(buildCalls, "utf8"), /exec -- go build -o .*gripi\.new\./);
  assert.deepEqual((await readFile(fixture.calls, "utf8")).trim().split("\n"), ["run", "run"]);
});

test("launcher discards a pending cutover from a different checkout", async () => {
  const fixture = await launcherFixture();
  const pending = path.join(fixture.project, "tmp", ".gripi-update-pending");
  await executable(fixture.gateway, "#!/bin/sh\nprintf 'old\n' >> \"$CALLS_PATH\"\n");
  await mkdir(pending, { recursive: true, mode: 0o700 });
  await writeFile(path.join(pending, "revision"), `${"b".repeat(40)}\n`);
  await executable(path.join(pending, "gripi"), "#!/bin/sh\nprintf 'new\n' >> \"$CALLS_PATH\"\n");
  await executable(path.join(fixture.fakeBin, "git"), `#!/bin/sh\nprintf '${"a".repeat(40)}\n'\n`);
  const result = run(fixture.launcher, [], { cwd: fixture.project, env: fixture.env });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(await readFile(fixture.calls, "utf8"), "old\n");
  await assert.rejects(stat(pending));
});

test("launcher requires HOME when no explicit restart path is configured", async () => {
  const fixture = await launcherFixture();
  await executable(fixture.gateway, "#!/bin/sh\nprintf 'unexpected\n' >> \"$CALLS_PATH\"\n");
  const env = { ...fixture.env, HOME: "", GRIPI_RESTART_PATH: "" };
  const result = run(fixture.launcher, [], { cwd: fixture.project, env });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /HOME or GRIPI_RESTART_PATH must be set/);
  await assert.rejects(readFile(fixture.calls));
});

test("launcher completes a matching interrupted update cutover", async () => {
  const fixture = await launcherFixture();
  const revision = "a".repeat(40);
  const pending = path.join(fixture.project, "tmp", ".gripi-update-pending");
  await executable(fixture.gateway, "#!/bin/sh\nprintf 'old\n' >> \"$CALLS_PATH\"\n");
  await mkdir(pending, { recursive: true, mode: 0o700 });
  await writeFile(path.join(pending, "revision"), `${revision}\n`);
  await executable(path.join(pending, "gripi"), "#!/bin/sh\nprintf 'new\n' >> \"$CALLS_PATH\"\n");
  await executable(path.join(fixture.fakeBin, "git"), `#!/bin/sh\nprintf '${revision}\n'\n`);
  const result = run(fixture.launcher, [], { cwd: fixture.project, env: fixture.env });
  assert.equal(result.status, 0, result.stderr);
  assert.equal(await readFile(fixture.calls, "utf8"), "new\n");
});

test("desktop installer requires an available FUSE 2 library only on Linux", async () => {
  const root = temporaryDirectory();
  const project = path.join(root, "project");
  const fakeBin = path.join(root, "fake-bin");
  const installer = path.join(project, "bin", "install-desktop");
  const buildStarted = path.join(root, "build-started");
  const fuseChecked = path.join(root, "fuse-checked");
  const fuseRoots = path.join(root, "fuse-roots");
  await mkdir(path.dirname(installer), { recursive: true });
  await mkdir(fakeBin, { recursive: true });
  await mkdir(fuseRoots, { recursive: true });
  await cp(path.join(repoRoot, "bin/install-desktop"), installer);
  await chmod(installer, 0o755);
  await executable(path.join(fakeBin, "uname"), "#!/bin/sh\necho Linux\n");
  await executable(path.join(fakeBin, "ldconfig"), "#!/bin/sh\nexit 1\n");
  await executable(path.join(fakeBin, "mise"), "#!/bin/sh\ntouch \"$BUILD_STARTED\"\nexit 1\n");
  await executable(path.join(fakeBin, "npm"), "#!/bin/sh\nexit 0\n");
  const env = { ...process.env, PATH: `${fakeBin}:${process.env.PATH}`, BUILD_STARTED: buildStarted, FUSE_CHECKED: fuseChecked, GRIPI_FUSE_LIBRARY_ROOTS: fuseRoots };

  let result = run(installer, [], { env });
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /requires FUSE 2/);
  await assert.rejects(readFile(buildStarted));

  await executable(path.join(fakeBin, "ldconfig"), "#!/bin/sh\nprintf 'libfuse.so.2 (libc6) => /usr/lib/libfuse.so.2\n'\nindex=0\nwhile [ \"$index\" -lt 10000 ]; do printf 'unrelated library %s\\n' \"$index\"; index=$((index + 1)); done\n");
  result = run(installer, [], { env });
  assert.notEqual(result.status, 0);
  assert.doesNotMatch(result.stderr, /requires FUSE 2/);
  assert.equal(await readFile(buildStarted, "utf8"), "");

  await executable(path.join(fakeBin, "ldconfig"), "#!/bin/sh\nexit 1\n");
  await writeFile(path.join(fuseRoots, "libfuse.so.2"), "");
  result = run(installer, [], { env });
  assert.notEqual(result.status, 0);
  assert.doesNotMatch(result.stderr, /requires FUSE 2/);

  await executable(path.join(fakeBin, "uname"), "#!/bin/sh\necho Darwin\n");
  await executable(path.join(fakeBin, "ldconfig"), "#!/bin/sh\ntouch \"$FUSE_CHECKED\"\nexit 1\n");
  await rm(fuseChecked, { force: true });
  result = run(installer, [], { env });
  assert.notEqual(result.status, 0);
  assert.doesNotMatch(result.stderr, /FUSE 2/);
  await assert.rejects(readFile(fuseChecked));
});
