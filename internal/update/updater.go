package update

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const maxCommandOutputBytes = 32 * 1024

const (
	gitStepTimeout       = 30 * time.Second
	fetchStepTimeout     = 2 * time.Minute
	worktreeStepTimeout  = 2 * time.Minute
	miseInstallTimeout   = 5 * time.Minute
	testTimeout          = 15 * time.Minute
	goBuildTimeout       = 5 * time.Minute
	updateCleanupTimeout = 5 * time.Second
)

type Status struct {
	State          string
	Reason         string
	CurrentSHA     string
	TargetSHA      string
	TargetRevision string
	AheadCount     int
	BehindCount    int
	Summary        string
	Message        string
}

type Result struct {
	State      string
	Status     Status
	RolledBack bool
	Message    string
}

type commandResult struct {
	stdout, stderr string
	success        bool
	timedOut       bool
	exitStatus     int
}

type Updater struct {
	Directory     string
	BinaryPath    string
	StageParent   string
	Validate      func(context.Context, string, string) error
	Install       func(string, string) error
	AdmitCutover  func() bool
	ResumeCutover func()
	Supported     bool
}

func NewUpdater(directory string) *Updater {
	return &Updater{
		Directory:  directory,
		BinaryPath: filepath.Join(directory, "tmp", "gripi"),
		Validate:   validateCheckout,
		Install:    installBinary,
		Supported:  supportsPlatform(runtime.GOOS),
	}
}

func supportsPlatform(name string) bool { return name == "linux" || name == "darwin" }

func (updater *Updater) Status(ctx context.Context) Status {
	if !updater.Supported {
		return Status{State: "blocked", Reason: "platform", Message: "Self-update is unavailable on this operating system"}
	}
	branch := updater.git(ctx, gitStepTimeout, "branch", "--show-current")
	if !branch.success {
		return operationalError("repository", branch, "Could not determine the current Git branch", "", "")
	}
	currentRevision, failure := updater.revision(ctx, "HEAD")
	if failure != nil {
		return *failure
	}
	currentSHA := shorten(currentRevision)
	if strings.TrimSpace(branch.stdout) != "master" {
		return Status{State: "blocked", Reason: "branch", CurrentSHA: currentSHA, Message: "Updates require the master branch"}
	}
	fetch := updater.git(ctx, fetchStepTimeout, "fetch", "--no-tags", "origin", "master")
	if !fetch.success {
		return operationalError("fetch", fetch, "Could not fetch origin master", currentSHA, "")
	}
	targetRevision, failure := updater.revision(ctx, "origin/master")
	if failure != nil {
		return *failure
	}
	targetSHA := shorten(targetRevision)
	dirty := updater.git(ctx, gitStepTimeout, "status", "--porcelain", "--untracked-files=all")
	if !dirty.success {
		return operationalError("repository", dirty, "Could not inspect the checkout", currentSHA, targetSHA)
	}
	if dirty.stdout != "" {
		return Status{State: "blocked", Reason: "dirty", CurrentSHA: currentSHA, TargetSHA: targetSHA, TargetRevision: targetRevision, Message: "The checkout has tracked or untracked changes"}
	}
	if currentRevision == targetRevision {
		return Status{State: "up_to_date", CurrentSHA: currentSHA, TargetSHA: targetSHA, TargetRevision: targetRevision}
	}
	headAncestor := updater.git(ctx, gitStepTimeout, "merge-base", "--is-ancestor", "HEAD", "origin/master")
	if headAncestor.success {
		behind, err := updater.commitCount(ctx, "HEAD..origin/master")
		if err != nil {
			return *err
		}
		return Status{State: "available", CurrentSHA: currentSHA, TargetSHA: targetSHA, TargetRevision: targetRevision, BehindCount: behind, Summary: updater.commitSummary(ctx, "HEAD..origin/master"), Message: fmt.Sprintf("%d update commit%s available", behind, plural(behind))}
	}
	if headAncestor.exitStatus != 1 {
		return operationalError("repository", headAncestor, "Could not compare Git revisions", currentSHA, targetSHA)
	}
	targetAncestor := updater.git(ctx, gitStepTimeout, "merge-base", "--is-ancestor", "origin/master", "HEAD")
	if targetAncestor.success {
		ahead, err := updater.commitCount(ctx, "origin/master..HEAD")
		if err != nil {
			return *err
		}
		return Status{State: "blocked", Reason: "ahead", CurrentSHA: currentSHA, TargetSHA: targetSHA, TargetRevision: targetRevision, AheadCount: ahead, Summary: updater.commitSummary(ctx, "origin/master..HEAD"), Message: fmt.Sprintf("The checkout has %d local commit%s", ahead, plural(ahead))}
	}
	if targetAncestor.exitStatus != 1 {
		return operationalError("repository", targetAncestor, "Could not compare Git revisions", currentSHA, targetSHA)
	}
	ahead, aheadErr := updater.commitCount(ctx, "origin/master..HEAD")
	if aheadErr != nil {
		return *aheadErr
	}
	behind, behindErr := updater.commitCount(ctx, "HEAD..origin/master")
	if behindErr != nil {
		return *behindErr
	}
	return Status{State: "blocked", Reason: "diverged", CurrentSHA: currentSHA, TargetSHA: targetSHA, TargetRevision: targetRevision, AheadCount: ahead, BehindCount: behind, Summary: updater.commitSummary(ctx, "HEAD...origin/master"), Message: "The checkout has diverged from origin/master"}
}

func (updater *Updater) Update(ctx context.Context) Result {
	precondition := updater.Status(ctx)
	if precondition.State != "available" {
		return Result{State: precondition.State, Status: precondition, Message: precondition.Message}
	}
	old, failure := updater.revision(ctx, "HEAD")
	if failure != nil {
		return Result{State: "error", Status: precondition, Message: failure.Message}
	}
	if err := validateBinaryDestination(updater.BinaryPath); err != nil {
		return Result{State: "error", Status: precondition, Message: "Unsafe gateway binary destination: " + err.Error()}
	}

	stageParent := updater.StageParent
	if stageParent == "" {
		stageParent = filepath.Dir(updater.Directory)
	}
	stageRoot, err := os.MkdirTemp(stageParent, ".gripi-update-")
	if err != nil {
		return Result{State: "error", Status: precondition, Message: "Could not create private update stage: " + err.Error()}
	}
	if err := os.Chmod(stageRoot, 0700); err != nil {
		_ = os.RemoveAll(stageRoot)
		return Result{State: "error", Status: precondition, Message: "Could not secure update stage: " + err.Error()}
	}
	worktree := filepath.Join(stageRoot, "worktree")
	installDirectory := filepath.Join(stageRoot, "install")
	if err := os.Mkdir(installDirectory, 0700); err != nil {
		_ = os.RemoveAll(stageRoot)
		return Result{State: "error", Status: precondition, Message: "Could not create private install stage: " + err.Error()}
	}
	stagedBinary := filepath.Join(installDirectory, "gripi")
	added := false
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), updateCleanupTimeout)
		defer cancel()
		if added {
			_ = updater.git(cleanup, updateCleanupTimeout, "worktree", "remove", "--force", worktree).success
		}
		_ = os.RemoveAll(stageRoot)
		_ = updater.git(cleanup, updateCleanupTimeout, "worktree", "prune").success
	}()

	worktreeResult := updater.git(ctx, worktreeStepTimeout, "worktree", "add", "--detach", worktree, precondition.TargetRevision)
	if !worktreeResult.success {
		return Result{State: "error", Status: precondition, Message: commandError("Could not stage updated checkout", worktreeResult)}
	}
	added = true
	if err := updater.Validate(ctx, worktree, stagedBinary); err != nil {
		return Result{State: "dependency_failed", Status: precondition, Message: "Update validation failed before changing the live checkout: " + err.Error()}
	}
	if err := validateStagedBinary(stagedBinary); err != nil {
		return Result{State: "dependency_failed", Status: precondition, Message: "Updated gateway build is invalid: " + err.Error()}
	}
	unlock, err := updater.lockCheckout(ctx)
	if err != nil {
		return Result{State: "error", Status: precondition, Message: "Could not lock the live checkout for update: " + err.Error()}
	}
	defer unlock()
	completed := false
	if updater.AdmitCutover != nil {
		if !updater.AdmitCutover() {
			return Result{State: "error", Status: precondition, Message: "Active Pi work started during validation; retry the update when it is idle"}
		}
		defer func() {
			if !completed && updater.ResumeCutover != nil {
				updater.ResumeCutover()
			}
		}()
	}
	if err := updater.confirmUnchangedCheckout(ctx, old); err != nil {
		return Result{State: "error", Status: precondition, Message: err.Error()}
	}
	pendingBinary, pendingDirectory, err := updater.preparePendingBinary(stagedBinary, precondition.TargetRevision)
	if err != nil {
		return Result{State: "error", Status: precondition, Message: "Could not prepare recoverable gateway cutover: " + err.Error()}
	}
	defer os.RemoveAll(pendingDirectory)

	forward := updater.git(ctx, gitStepTimeout, "merge", "--ff-only", precondition.TargetRevision)
	if !forward.success {
		updateErr := errors.New(commandError("Could not fast-forward to origin/master", forward))
		if updater.checkoutAt(precondition.TargetRevision) {
			return updater.rollbackAfterFailure(precondition, old, updateErr)
		}
		return Result{State: "error", Status: precondition, Message: updateErr.Error()}
	}
	if err := ctx.Err(); err != nil {
		return updater.rollbackAfterFailure(precondition, old, fmt.Errorf("update timed out after checkout cutover: %w", err))
	}
	if !updater.checkoutAt(precondition.TargetRevision) {
		return Result{State: "error", Status: precondition, Message: "Live checkout changed unexpectedly during update; the staged binary was not installed"}
	}
	if err := ctx.Err(); err != nil {
		return updater.rollbackAfterFailure(precondition, old, fmt.Errorf("update timed out before binary installation: %w", err))
	}
	if err := updater.Install(pendingBinary, updater.BinaryPath); err == nil {
		completed = true
		return Result{State: "updated", Status: precondition, Message: "Updated to " + precondition.TargetSHA}
	} else {
		return updater.rollbackAfterFailure(precondition, old, err)
	}
}

func (updater *Updater) preparePendingBinary(stagedBinary, revision string) (string, string, error) {
	directory := filepath.Join(filepath.Dir(updater.BinaryPath), ".gripi-update-pending")
	if info, err := os.Lstat(directory); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", "", errors.New("pending cutover path must be a real directory")
		}
		if err := os.RemoveAll(directory); err != nil {
			return "", "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if err := os.Mkdir(directory, 0700); err != nil {
		return "", "", err
	}
	pendingBinary := filepath.Join(directory, "gripi")
	if err := os.Rename(stagedBinary, pendingBinary); err != nil {
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	binary, err := os.Open(pendingBinary)
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	if err := binary.Sync(); err != nil {
		binary.Close()
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	if err := binary.Close(); err != nil {
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	revisionPath := filepath.Join(directory, "revision")
	if err := os.WriteFile(revisionPath, []byte(revision+"\n"), 0600); err != nil {
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	revisionFile, err := os.Open(revisionPath)
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	if err := revisionFile.Sync(); err != nil {
		revisionFile.Close()
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	if err := revisionFile.Close(); err != nil {
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	journal, err := os.Open(directory)
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	if err := journal.Sync(); err != nil {
		journal.Close()
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	if err := journal.Close(); err != nil {
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	parent, err := os.Open(filepath.Dir(directory))
	if err != nil {
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	if err := parent.Sync(); err != nil {
		parent.Close()
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	if err := parent.Close(); err != nil {
		_ = os.RemoveAll(directory)
		return "", "", err
	}
	return pendingBinary, directory, nil
}

func (updater *Updater) lockCheckout(ctx context.Context) (func(), error) {
	result := updater.git(ctx, gitStepTimeout, "rev-parse", "--git-path", "gripi-update.lock")
	if !result.success {
		return nil, errors.New(commandError("Could not locate the Git update lock", result))
	}
	path := strings.TrimSpace(result.stdout)
	if !filepath.IsAbs(path) {
		path = filepath.Join(updater.Directory, path)
	}
	lockContext, cancel := context.WithTimeout(ctx, gitStepTimeout)
	unlock, err := acquireCheckoutLock(lockContext, path)
	cancel()
	return unlock, err
}

func (updater *Updater) confirmUnchangedCheckout(ctx context.Context, expectedRevision string) error {
	revision, failure := updater.revision(ctx, "HEAD")
	if failure != nil {
		return errors.New(failure.Message)
	}
	if revision != expectedRevision {
		return errors.New("Live checkout changed during validation; update was not applied")
	}
	status := updater.git(ctx, gitStepTimeout, "status", "--porcelain", "--untracked-files=all")
	if !status.success {
		return errors.New(commandError("Could not revalidate the live checkout", status))
	}
	if status.stdout != "" {
		return errors.New("Live checkout changed during validation; update was not applied")
	}
	return nil
}

func (updater *Updater) checkoutAt(expected string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), gitStepTimeout)
	defer cancel()
	revision, failure := updater.revision(ctx, "HEAD")
	return failure == nil && revision == expected
}

func (updater *Updater) rollbackAfterFailure(status Status, revision string, updateErr error) Result {
	ctx, cancel := context.WithTimeout(context.Background(), gitStepTimeout)
	defer cancel()
	if err := updater.confirmCleanCheckoutAt(ctx, status.TargetRevision); err != nil {
		return Result{State: "rollback_failed", Status: status, Message: updateErr.Error() + ". The checkout was not reset because it changed after cutover: " + err.Error()}
	}
	rollback := updater.git(ctx, gitStepTimeout, "reset", "--hard", revision)
	if !rollback.success {
		return Result{State: "rollback_failed", Status: status, Message: updateErr.Error() + ". " + commandError("The checkout could not be rolled back", rollback)}
	}
	return Result{State: "dependency_failed", Status: status, RolledBack: true, Message: "Gateway update failed; restored " + status.CurrentSHA + ": " + updateErr.Error()}
}

func (updater *Updater) confirmCleanCheckoutAt(ctx context.Context, expectedRevision string) error {
	revision, failure := updater.revision(ctx, "HEAD")
	if failure != nil {
		return errors.New(failure.Message)
	}
	if revision != expectedRevision {
		return errors.New("revision changed")
	}
	status := updater.git(ctx, gitStepTimeout, "status", "--porcelain", "--untracked-files=all")
	if !status.success {
		return errors.New(commandError("could not inspect checkout", status))
	}
	if status.stdout != "" {
		return errors.New("working tree changed")
	}
	return nil
}

func (updater *Updater) git(ctx context.Context, timeout time.Duration, arguments ...string) commandResult {
	return runCommand(ctx, updater.Directory, timeout, "git", arguments...)
}

func (updater *Updater) revision(ctx context.Context, name string) (string, *Status) {
	result := updater.git(ctx, gitStepTimeout, "rev-parse", name)
	if result.success {
		return strings.TrimSpace(result.stdout), nil
	}
	failure := operationalError("repository", result, "Could not resolve "+name, "", "")
	return "", &failure
}

func (updater *Updater) commitCount(ctx context.Context, value string) (int, *Status) {
	result := updater.git(ctx, gitStepTimeout, "rev-list", "--count", value)
	if result.success {
		count, err := strconv.Atoi(strings.TrimSpace(result.stdout))
		if err == nil {
			return count, nil
		}
	}
	failure := operationalError("repository", result, "Could not count commits", "", "")
	return 0, &failure
}

func (updater *Updater) commitSummary(ctx context.Context, value string) string {
	result := updater.git(ctx, gitStepTimeout, "log", "--format=%h %s", value)
	if result.success {
		return strings.TrimSpace(result.stdout)
	}
	return ""
}

func validateCheckout(ctx context.Context, directory, target string) error {
	steps := []struct {
		timeout time.Duration
		args    []string
	}{
		{miseInstallTimeout, []string{"mise", "install"}},
		{testTimeout, []string{"mise", "run", "test"}},
		{goBuildTimeout, []string{"mise", "exec", "--", "go", "build", "-o", target, "./cmd/gripi"}},
	}
	for _, step := range steps {
		result := runCommand(ctx, directory, step.timeout, step.args[0], step.args[1:]...)
		if !result.success {
			return errors.New(commandError("Could not validate updated checkout", result))
		}
	}
	return nil
}

func validateBinaryDestination(target string) error {
	directory := filepath.Dir(target)
	if info, err := os.Lstat(directory); err != nil {
		return err
	} else if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("tmp directory must be a real directory")
	}
	if info, err := os.Lstat(target); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return errors.New("binary must be a regular non-symlink file")
		}
	} else {
		return err
	}
	return nil
}

func validateStagedBinary(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode()&0100 == 0 {
		return errors.New("staged binary must be an executable regular file")
	}
	return nil
}

func installBinary(source, target string) error {
	if err := validateBinaryDestination(target); err != nil {
		return err
	}
	if err := validateStagedBinary(source); err != nil {
		return err
	}
	if err := os.Rename(source, target); err != nil {
		return fmt.Errorf("atomically install updated gateway: %w", err)
	}
	if directory, err := os.Open(filepath.Dir(target)); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func DiscoverCheckout(executable, workingDirectory string, allowDevelopment bool) (string, error) {
	executable, err := filepath.Abs(executable)
	if err != nil {
		return "", err
	}
	info, statErr := os.Lstat(executable)
	if statErr != nil {
		return "", statErr
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", errors.New("gateway executable must be a regular non-symlink file")
	}
	if filepath.Base(executable) == "gripi" && filepath.Base(filepath.Dir(executable)) == "tmp" {
		root := filepath.Dir(filepath.Dir(executable))
		if err := verifyGitRoot(root); err != nil {
			return "", fmt.Errorf("verify installed gateway checkout: %w", err)
		}
		if err := validateBinaryDestination(executable); err != nil {
			return "", err
		}
		return root, nil
	}
	if !allowDevelopment {
		return "", errors.New("production gateway executable must be installed at <checkout>/tmp/gripi")
	}
	root, err := discoverGitRoot(workingDirectory)
	if err != nil {
		return "", fmt.Errorf("development gateway requires a verified Git root: %w", err)
	}
	return root, nil
}

func verifyGitRoot(directory string) error {
	root, err := discoverGitRoot(directory)
	if err != nil {
		return err
	}
	directory, err = filepath.Abs(directory)
	if err != nil {
		return err
	}
	if root != directory {
		return errors.New("working directory is not the Git root")
	}
	return nil
}

func discoverGitRoot(directory string) (string, error) {
	result := runCommand(context.Background(), directory, gitStepTimeout, "git", "rev-parse", "--show-toplevel")
	if !result.success {
		return "", errors.New(commandError("not a Git checkout", result))
	}
	return filepath.Abs(strings.TrimSpace(result.stdout))
}

type tailWriter struct {
	buffer []byte
	limit  int
}

func (writer *tailWriter) Write(value []byte) (int, error) {
	length := len(value)
	if length >= writer.limit {
		writer.buffer = append(writer.buffer[:0], value[length-writer.limit:]...)
		return length, nil
	}
	overflow := len(writer.buffer) + length - writer.limit
	if overflow > 0 {
		copy(writer.buffer, writer.buffer[overflow:])
		writer.buffer = writer.buffer[:len(writer.buffer)-overflow]
	}
	writer.buffer = append(writer.buffer, value...)
	return length, nil
}

func (writer *tailWriter) String() string { return string(writer.buffer) }

func runCommand(parent context.Context, directory string, timeout time.Duration, name string, arguments ...string) commandResult {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	command.WaitDelay = 100 * time.Millisecond
	configureCommand(command)
	stdout := &tailWriter{limit: maxCommandOutputBytes}
	stderr := &tailWriter{limit: maxCommandOutputBytes}
	command.Stdout = io.Writer(stdout)
	command.Stderr = io.Writer(stderr)
	err := command.Run()
	result := commandResult{stdout: stdout.String(), stderr: stderr.String(), success: err == nil, timedOut: errors.Is(ctx.Err(), context.DeadlineExceeded)}
	if exit := new(exec.ExitError); errors.As(err, &exit) {
		result.exitStatus = exit.ExitCode()
	} else if err != nil {
		result.exitStatus = -1
		if result.stderr == "" {
			result.stderr = err.Error()
		}
	}
	return result
}

func shorten(value string) string {
	if len(value) > 8 {
		return value[:8]
	}
	return value
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

func operationalError(reason string, result commandResult, fallback, current, target string) Status {
	return Status{State: "error", Reason: reason, CurrentSHA: current, TargetSHA: target, Message: commandError(fallback, result)}
}

func commandError(fallback string, result commandResult) string {
	if result.timedOut {
		return fallback + " timed out"
	}
	detail := strings.TrimSpace(result.stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.stdout)
	}
	if detail == "" {
		return fallback
	}
	return fallback + ": " + detail
}
