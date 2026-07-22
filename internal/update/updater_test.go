package update

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type gitFixture struct{ root, origin, upstream, checkout string }

func TestUpdaterSupportIsRestrictedToDocumentedPlatforms(t *testing.T) {
	for platform, expected := range map[string]bool{"linux": true, "darwin": true, "windows": false, "freebsd": false} {
		if supported := supportsPlatform(platform); supported != expected {
			t.Fatalf("supportsPlatform(%q) = %v", platform, supported)
		}
	}
}

func TestUpdaterReportsUnsupportedPlatformsWithoutTouchingGit(t *testing.T) {
	updater := NewUpdater(t.TempDir())
	updater.Supported = false
	status := updater.Status(context.Background())
	if status.State != "blocked" || status.Reason != "platform" {
		t.Fatalf("status = %+v", status)
	}
}

func TestUpdaterReportsSafeFastForwardsAndBlocksDirtyCheckouts(t *testing.T) {
	fixture := newGitFixture(t)
	upstreamCommit(t, fixture, "update.txt", "new\n", "Add update")
	updater := NewUpdater(fixture.checkout)
	status := updater.Status(context.Background())
	if status.State != "available" || status.BehindCount != 1 || !strings.Contains(status.Summary, "Add update") {
		t.Fatalf("status = %+v", status)
	}
	if err := os.WriteFile(filepath.Join(fixture.checkout, "dirty.txt"), []byte("dirty"), 0644); err != nil {
		t.Fatal(err)
	}
	status = updater.Status(context.Background())
	if status.State != "blocked" || status.Reason != "dirty" {
		t.Fatalf("dirty status = %+v", status)
	}
}

func TestUpdaterValidatesIsolatedTargetBeforeFastForwardAndAtomicallyInstallsBinary(t *testing.T) {
	fixture := newGitFixture(t)
	old := installFixtureBinary(t, fixture.checkout, "old binary\n")
	target := upstreamCommit(t, fixture, "app.txt", "updated\n", "Add update")
	validated := ""
	updater := NewUpdater(fixture.checkout)
	updater.StageParent = fixture.root
	updater.Validate = func(_ context.Context, directory, binary string) error {
		validated = directory
		live, _ := os.ReadFile(filepath.Join(fixture.checkout, "app.txt"))
		staged, _ := os.ReadFile(filepath.Join(directory, "app.txt"))
		if string(live) != "initial\n" || string(staged) != "updated\n" {
			return errors.New("validation did not run before live checkout changed")
		}
		return os.WriteFile(binary, []byte("new binary\n"), 0700)
	}

	result := updater.Update(context.Background())

	if result.State != "updated" || validated == "" || validated == fixture.checkout {
		t.Fatalf("result = %+v, validated = %q", result, validated)
	}
	if got := gitOutput(t, fixture.checkout, "rev-parse", "HEAD"); got != target {
		t.Fatalf("HEAD = %s, target = %s", got, target)
	}
	if contents, _ := os.ReadFile(old); string(contents) != "new binary\n" {
		t.Fatalf("binary = %q", contents)
	}
	assertNoUpdateStages(t, fixture.root)
	assertNoPendingCutover(t, fixture.checkout)
}

func TestDefaultValidationUsesCanonicalChecksWithoutDependencyMutation(t *testing.T) {
	directory := t.TempDir()
	bin := filepath.Join(directory, "bin")
	if err := os.MkdirAll(bin, 0755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(directory, "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$VALIDATION_CALLS"
previous=''
for argument in "$@"; do
  if [ "$previous" = "-o" ]; then mkdir -p "$(dirname "$argument")"; printf built > "$argument"; fi
  previous="$argument"
done
`
	if err := os.WriteFile(filepath.Join(bin, "mise"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("VALIDATION_CALLS", record)
	target := filepath.Join(directory, "private", "gripi")
	if err := validateCheckout(context.Background(), directory, target); err != nil {
		t.Fatal(err)
	}
	calls, _ := os.ReadFile(record)
	for _, expected := range []string{"install", "run test", "exec -- go build -o " + target} {
		if !strings.Contains(string(calls), expected) {
			t.Fatalf("calls = %s; missing %q", calls, expected)
		}
	}
	if strings.Contains(string(calls), "npm") {
		t.Fatalf("validation mutated Node dependencies: %s", calls)
	}
}

func TestUpdaterValidationChecksNeedNoNodeModules(t *testing.T) {
	checkout := t.TempDir()
	root := filepath.Clean(filepath.Join("..", ".."))
	for _, directory := range []string{"bin", "demo", "pi_extensions", "public", "test"} {
		if err := os.CopyFS(filepath.Join(checkout, directory), os.DirFS(filepath.Join(root, directory))); err != nil {
			t.Fatal(err)
		}
	}
	packageJSON, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(checkout, "package.json"), packageJSON, 0600); err != nil {
		t.Fatal(err)
	}
	miseConfig, err := os.ReadFile(filepath.Join(root, ".mise.toml"))
	if err != nil {
		t.Fatal(err)
	}
	canonicalTask := `run = "go test ./... && npm run frontend:check && npm run scripts:check && npm run pi-extension:check"`
	if !strings.Contains(string(miseConfig), canonicalTask) {
		t.Fatalf("mise test task does not use all dependency-self-contained checks: %s", miseConfig)
	}
	piExecutable, err := exec.LookPath("pi")
	if err != nil {
		t.Fatalf("Pi CLI is required for canonical validation: %v", err)
	}
	for _, script := range []string{"frontend:check", "scripts:check", "pi-extension:check"} {
		command := exec.Command("npm", "run", script)
		command.Dir = checkout
		command.Env = append(os.Environ(), "GRIPI_PI="+piExecutable)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("dependency-self-contained %s failed without node_modules: %v\n%s", script, err, output)
		}
	}
	if _, err := os.Stat(filepath.Join(checkout, "node_modules")); !os.IsNotExist(err) {
		t.Fatalf("validation created node_modules: %v", err)
	}
}

func TestBrowserWorkflowInstallsPiBeforeCanonicalValidation(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "browser_e2e.yml"))
	if err != nil {
		t.Fatal(err)
	}
	piInstall := strings.Index(string(workflow), "npm install --global @earendil-works/pi-coding-agent@")
	canonicalValidation := strings.Index(string(workflow), "mise run test")
	if piInstall < 0 || canonicalValidation < 0 || piInstall > canonicalValidation {
		t.Fatalf("browser workflow must install Pi before canonical validation: %s", workflow)
	}
}

func TestUpdaterRequiresFreshCutoverAdmissionAndResumesItAfterFailure(t *testing.T) {
	fixture := newGitFixture(t)
	installFixtureBinary(t, fixture.checkout, "old binary\n")
	old := gitOutput(t, fixture.checkout, "rev-parse", "HEAD")
	upstreamCommit(t, fixture, "app.txt", "updated\n", "Add update")
	updater := NewUpdater(fixture.checkout)
	updater.StageParent = fixture.root
	updater.Validate = func(_ context.Context, _ string, staged string) error {
		return os.WriteFile(staged, []byte("new binary\n"), 0700)
	}
	resumed := false
	updater.AdmitCutover = func() bool { return false }
	updater.ResumeCutover = func() { resumed = true }

	result := updater.Update(context.Background())

	if result.State != "error" || !strings.Contains(result.Message, "Active Pi work") || resumed {
		t.Fatalf("result = %+v, resumed = %v", result, resumed)
	}
	if got := gitOutput(t, fixture.checkout, "rev-parse", "HEAD"); got != old {
		t.Fatalf("HEAD = %s, want %s", got, old)
	}
}

func TestUpdaterRefusesCutoverWhenLiveCheckoutChangesDuringValidation(t *testing.T) {
	fixture := newGitFixture(t)
	binary := installFixtureBinary(t, fixture.checkout, "old binary\n")
	upstreamCommit(t, fixture, "app.txt", "updated\n", "Add update")
	updater := NewUpdater(fixture.checkout)
	updater.StageParent = fixture.root
	updater.Validate = func(_ context.Context, _ string, staged string) error {
		if err := os.WriteFile(filepath.Join(fixture.checkout, "app.txt"), []byte("local work\n"), 0644); err != nil {
			return err
		}
		return os.WriteFile(staged, []byte("new binary\n"), 0700)
	}

	result := updater.Update(context.Background())

	if result.State != "error" || !strings.Contains(result.Message, "changed during validation") {
		t.Fatalf("result = %+v", result)
	}
	if contents, _ := os.ReadFile(filepath.Join(fixture.checkout, "app.txt")); string(contents) != "local work\n" {
		t.Fatalf("local work was changed: %q", contents)
	}
	if contents, _ := os.ReadFile(binary); string(contents) != "old binary\n" {
		t.Fatalf("binary = %q", contents)
	}
}

func TestUpdaterValidationFailureLeavesLiveCheckoutAndBinaryUntouched(t *testing.T) {
	fixture := newGitFixture(t)
	binary := installFixtureBinary(t, fixture.checkout, "old binary\n")
	old := gitOutput(t, fixture.checkout, "rev-parse", "HEAD")
	upstreamCommit(t, fixture, "app.txt", "updated\n", "Break build")
	updater := NewUpdater(fixture.checkout)
	updater.StageParent = fixture.root
	updater.Validate = func(context.Context, string, string) error { return os.ErrInvalid }

	result := updater.Update(context.Background())

	if result.State != "dependency_failed" || result.RolledBack {
		t.Fatalf("result = %+v", result)
	}
	if got := gitOutput(t, fixture.checkout, "rev-parse", "HEAD"); got != old {
		t.Fatalf("HEAD = %s, want %s", got, old)
	}
	if contents, _ := os.ReadFile(binary); string(contents) != "old binary\n" {
		t.Fatalf("binary = %q", contents)
	}
	assertNoUpdateStages(t, fixture.root)
	assertNoPendingCutover(t, fixture.checkout)
}

func TestUpdaterRollsBackTrackedCheckoutAndPreservesBinaryAfterPostCheckoutFailure(t *testing.T) {
	fixture := newGitFixture(t)
	binary := installFixtureBinary(t, fixture.checkout, "old binary\n")
	old := gitOutput(t, fixture.checkout, "rev-parse", "HEAD")
	upstreamCommit(t, fixture, "app.txt", "updated\n", "Add update")
	updater := NewUpdater(fixture.checkout)
	updater.StageParent = fixture.root
	updater.Validate = func(_ context.Context, _ string, staged string) error {
		return os.WriteFile(staged, []byte("new binary\n"), 0700)
	}
	updater.Install = func(string, string) error { return errors.New("install failed") }
	resumed := false
	updater.AdmitCutover = func() bool { return true }
	updater.ResumeCutover = func() { resumed = true }

	result := updater.Update(context.Background())

	if result.State != "dependency_failed" || !result.RolledBack || !strings.Contains(result.Message, "install failed") || !resumed {
		t.Fatalf("result = %+v", result)
	}
	if got := gitOutput(t, fixture.checkout, "rev-parse", "HEAD"); got != old {
		t.Fatalf("rollback HEAD = %s, want %s", got, old)
	}
	if contents, _ := os.ReadFile(binary); string(contents) != "old binary\n" {
		t.Fatalf("binary = %q", contents)
	}
	assertNoUpdateStages(t, fixture.root)
	assertNoPendingCutover(t, fixture.checkout)
}

func TestUpdaterRejectsSymlinkedTmpAndBinaryDestinations(t *testing.T) {
	for _, target := range []string{"tmp", "binary"} {
		t.Run(target, func(t *testing.T) {
			root := t.TempDir()
			realDirectory := filepath.Join(root, "real")
			if err := os.Mkdir(realDirectory, 0700); err != nil {
				t.Fatal(err)
			}
			if target == "tmp" {
				if err := os.Symlink(realDirectory, filepath.Join(root, "tmp")); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(filepath.Join(root, "tmp"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(realDirectory, "gripi"), filepath.Join(root, "tmp", "gripi")); err != nil {
					t.Fatal(err)
				}
			}
			if err := validateBinaryDestination(filepath.Join(root, "tmp", "gripi")); err == nil {
				t.Fatal("symlinked destination accepted")
			}
		})
	}
}

func TestCheckoutDiscoveryAnchorsInstalledBinaryAndRequiresVerifiedGitRootForFallback(t *testing.T) {
	fixture := newGitFixture(t)
	binary := installFixtureBinary(t, fixture.checkout, "binary")
	root, err := DiscoverCheckout(binary, t.TempDir(), false)
	if err != nil || root != fixture.checkout {
		t.Fatalf("installed root = %q, %v", root, err)
	}
	developmentExecutable := filepath.Join(t.TempDir(), "go-build", "gripi-dev")
	if err := os.MkdirAll(filepath.Dir(developmentExecutable), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(developmentExecutable, []byte("dev"), 0700); err != nil {
		t.Fatal(err)
	}
	if root, err := DiscoverCheckout(developmentExecutable, fixture.checkout, true); err != nil || root != fixture.checkout {
		t.Fatalf("development root = %q, %v", root, err)
	}
	if root, err := DiscoverCheckout(developmentExecutable, filepath.Join(fixture.checkout, "tmp"), true); err != nil || root != fixture.checkout {
		t.Fatalf("development subdirectory root = %q, %v", root, err)
	}
	if _, err := DiscoverCheckout(developmentExecutable, fixture.checkout, false); err == nil {
		t.Fatal("production development fallback accepted")
	}
	if _, err := DiscoverCheckout(developmentExecutable, t.TempDir(), true); err == nil {
		t.Fatal("non-Git development fallback accepted")
	}
	linked := filepath.Join(fixture.root, "linked-gripi")
	if err := os.Symlink(binary, linked); err != nil {
		t.Fatal(err)
	}
	if _, err := DiscoverCheckout(linked, fixture.checkout, true); err == nil {
		t.Fatal("symlinked executable accepted")
	}
}

func TestCheckoutLockSerializesCutoversAndReleasesAfterOwnerFinishes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update.lock")
	unlock, err := acquireCheckoutLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if secondUnlock, err := acquireCheckoutLock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		if secondUnlock != nil {
			secondUnlock()
		}
		t.Fatalf("second lock error = %v", err)
	}
	cancel()
	unlock()
	unlock, err = acquireCheckoutLock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}

func TestRunCommandBoundsOutputToTailAndReportsDeadline(t *testing.T) {
	result := runCommand(context.Background(), t.TempDir(), time.Second, "sh", "-c", "printf prefix; head -c 100000 /dev/zero | tr '\\0' x; printf tail >&2; exit 7")
	if len(result.stdout) > maxCommandOutputBytes || len(result.stderr) > maxCommandOutputBytes || !strings.HasSuffix(result.stderr, "tail") {
		t.Fatalf("output sizes = %d/%d, stderr suffix = %q", len(result.stdout), len(result.stderr), result.stderr[max(0, len(result.stderr)-20):])
	}
	started := time.Now()
	directory := t.TempDir()
	descendantEffect := filepath.Join(directory, "descendant-finished")
	timedOut := runCommand(context.Background(), directory, 20*time.Millisecond, "sh", "-c", "(sleep .3; touch \"$1\") & wait", "sh", descendantEffect)
	if timedOut.success || !timedOut.timedOut || !strings.Contains(commandError("step failed", timedOut), "timed out") {
		t.Fatalf("timeout result = %+v", timedOut)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timed-out command returned after %s", elapsed)
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(descendantEffect); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("timed-out command descendant survived: %v", err)
	}
}

func installFixtureBinary(t *testing.T, checkout, contents string) string {
	t.Helper()
	tmp := filepath.Join(checkout, "tmp")
	if err := os.MkdirAll(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(tmp, "gripi")
	if err := os.WriteFile(path, []byte(contents), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertNoPendingCutover(t *testing.T, checkout string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(checkout, "tmp", ".gripi-update-pending")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending cutover remains: %v", err)
	}
}

func assertNoUpdateStages(t *testing.T, parent string) {
	t.Helper()
	stages, err := filepath.Glob(filepath.Join(parent, ".gripi-update-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 0 {
		t.Fatalf("stages remain: %v", stages)
	}
}

func newGitFixture(t *testing.T) gitFixture {
	t.Helper()
	root := t.TempDir()
	fixture := gitFixture{root: root, origin: filepath.Join(root, "origin.git"), upstream: filepath.Join(root, "upstream"), checkout: filepath.Join(root, "gateway;touch injected")}
	gitRun(t, root, "init", "--bare", "--initial-branch=master", fixture.origin)
	gitRun(t, root, "init", "--initial-branch=master", fixture.upstream)
	gitRun(t, fixture.upstream, "config", "user.email", "gateway@example.test")
	gitRun(t, fixture.upstream, "config", "user.name", "Gateway Test")
	os.WriteFile(filepath.Join(fixture.upstream, "app.txt"), []byte("initial\n"), 0644)
	os.WriteFile(filepath.Join(fixture.upstream, ".gitignore"), []byte("/tmp/\n"), 0644)
	gitRun(t, fixture.upstream, "add", "app.txt", ".gitignore")
	gitRun(t, fixture.upstream, "commit", "-m", "Initial version")
	gitRun(t, fixture.upstream, "remote", "add", "origin", fixture.origin)
	gitRun(t, fixture.upstream, "push", "-u", "origin", "master")
	gitRun(t, root, "clone", fixture.origin, fixture.checkout)
	return fixture
}
func upstreamCommit(t *testing.T, fixture gitFixture, path, contents, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(fixture.upstream, path), []byte(contents), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, fixture.upstream, "add", path)
	gitRun(t, fixture.upstream, "commit", "-m", message)
	gitRun(t, fixture.upstream, "push", "origin", "master")
	return gitOutput(t, fixture.upstream, "rev-parse", "HEAD")
}
func gitRun(t *testing.T, directory string, args ...string) {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
}
func gitOutput(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
