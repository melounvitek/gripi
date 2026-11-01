package reconcile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// repo is a repository on main, or its copy in the shadow at the same paths under at.
type repo struct {
	gitDir string   // its common Git folder, as main names it
	at     string   // "" on main
	env    []string // for git, besides the gateway's
}

func (r repo) path(p string) string { return r.at + p }

// git runs git with the Git folder dir, as main names it, and the work tree root, if any, and returns its output.
func (r repo) git(ctx context.Context, stdin io.Reader, dir, root string, args ...string) (string, error) {
	args = append([]string{"--git-dir=" + r.path(dir)}, args...)
	if root != "" {
		args = append([]string{"--work-tree=" + r.path(root)}, args...)
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Stdin = stdin
	if r.env != nil {
		cmd.Env = append(os.Environ(), r.env...)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(stderr.Bytes()))
	}
	return string(out), nil
}

// hash returns the blob IDs of the files at paths, by their bytes as they are; with write, it also stores them.
func (r repo) hash(ctx context.Context, paths []string, write bool) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	var input strings.Builder
	for _, p := range paths {
		if strings.ContainsAny(p, "\n\r") || strings.HasPrefix(p, `"`) {
			p = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\r", `\r`).Replace(p) + `"`
		}
		input.WriteString(p + "\n")
	}
	args := []string{"hash-object", "--no-filters", "--stdin-paths"}
	if write {
		args = append(args, "-w")
	}
	out, err := r.git(ctx, strings.NewReader(input.String()), r.gitDir, "", args...)
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(out)
	if len(ids) != len(paths) {
		return nil, fmt.Errorf("git hash-object: %d IDs for %d files", len(ids), len(paths))
	}
	return ids, nil
}

// ref is a ref's object, and the commit that it leads to, if any.
type ref struct {
	OID    string `json:"oid"`
	Commit string `json:"commit,omitempty"`
}

// refs reads the refs that a return reconciles: not symbolic ones, Gripi's own, the stash or those of one worktree.
func (r repo) refs(ctx context.Context) (map[string]ref, error) {
	out, err := r.git(ctx, nil, r.gitDir, "", "for-each-ref", "--format=%(refname)\t%(objectname)\t%(objecttype)\t%(*objectname)\t%(*objecttype)\t%(symref)")
	if err != nil {
		return nil, err
	}
	refs := map[string]ref{}
	for line := range strings.Lines(out) {
		f := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
		if len(f) != 6 || f[5] != "" || !reconciled(f[0]) {
			continue
		}
		rf := ref{OID: f[1]}
		if f[2] == "commit" {
			rf.Commit = f[1]
		} else if f[4] == "commit" {
			rf.Commit = f[3]
		}
		refs[f[0]] = rf
	}
	return refs, nil
}

func reconciled(name string) bool {
	return name != "refs/stash" && !slices.ContainsFunc([]string{"refs/gripi/", "refs/bisect/", "refs/worktree/", "refs/rewritten/"}, func(prefix string) bool {
		return strings.HasPrefix(name, prefix)
	})
}

type stash struct {
	OID     string `json:"oid"`
	Message string `json:"message"`
}

// stashes lists the stash, newest first.
func (r repo) stashes(ctx context.Context) ([]stash, error) {
	if _, err := r.git(ctx, nil, r.gitDir, "", "rev-parse", "-q", "--verify", "refs/stash"); err != nil {
		return nil, nil
	}
	out, err := r.git(ctx, nil, r.gitDir, "", "log", "-g", "--format=%H %gs", "refs/stash")
	if err != nil {
		return nil, err
	}
	var list []stash
	for line := range strings.Lines(out) {
		oid, message, _ := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
		list = append(list, stash{OID: oid, Message: message})
	}
	return list, nil
}

type worktree struct {
	Root   string   `json:"root"`
	GitDir string   `json:"git_dir"` // its own Git folder: the common one for the main checkout
	Head   head     `json:"head"`
	Index  string   `json:"index,omitempty"` // as git ls-files -s -z lists it
	Dirty  []string `json:"dirty,omitempty"` // what git status lists, relative to Root
}

type head struct {
	Branch string `json:"branch,omitempty"` // empty when detached
	OID    string `json:"oid,omitempty"`    // empty on an unborn branch
}

// worktrees lists the registered worktrees, the main checkout first, with their HEADs.
func (r repo) worktrees(ctx context.Context, refs map[string]ref) ([]worktree, error) {
	list := []worktree{{Root: filepath.Dir(r.gitDir), GitDir: r.gitDir}}
	entries, err := os.ReadDir(r.path(filepath.Join(r.gitDir, "worktrees")))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, entry := range entries {
		dir := filepath.Join(r.gitDir, "worktrees", entry.Name())
		gitFile, err := os.ReadFile(r.path(filepath.Join(dir, "gitdir")))
		if err != nil {
			continue // not a worktree's
		}
		dotGit := strings.TrimSpace(string(gitFile))
		if !filepath.IsAbs(dotGit) { // with worktree.useRelativePaths
			dotGit = filepath.Join(dir, dotGit)
		}
		list = append(list, worktree{Root: filepath.Dir(dotGit), GitDir: dir})
	}
	for i := range list {
		if list[i].Head, err = r.head(ctx, list[i].GitDir, refs); err != nil {
			return nil, err
		}
	}
	return list, nil
}

func (r repo) head(ctx context.Context, dir string, refs map[string]ref) (head, error) {
	if out, err := r.git(ctx, nil, dir, "", "symbolic-ref", "-q", "HEAD"); err == nil {
		branch := strings.TrimSpace(out)
		return head{Branch: branch, OID: refs[branch].OID}, nil
	}
	out, err := r.git(ctx, nil, dir, "", "rev-parse", "-q", "--verify", "HEAD")
	return head{OID: strings.TrimSpace(out)}, err
}

func (r repo) index(ctx context.Context, w worktree) (string, error) {
	return r.git(ctx, nil, w.GitDir, w.Root, "ls-files", "-s", "-z")
}

// dirty lists what git status shows in the worktree on main, ignored files aside.
func dirty(ctx context.Context, w worktree) ([]string, error) {
	out, err := repo{}.git(ctx, nil, w.GitDir, w.Root, "--no-optional-locks", "status", "--porcelain", "-z", "--untracked-files=all", "--no-renames")
	if err != nil {
		return nil, err
	}
	var paths []string
	for entry := range strings.SplitSeq(out, "\x00") {
		if len(entry) > 3 {
			paths = append(paths, entry[3:])
		}
	}
	return paths, nil
}

// blobs returns the blobs that an index lists.
func blobs(index string) []string {
	var ids []string
	for entry := range strings.SplitSeq(index, "\x00") {
		if f := strings.Fields(entry); len(f) >= 2 && f[0] != "160000" {
			ids = append(ids, f[1])
		}
	}
	return ids
}

// missing returns those of ids that the repository lacks.
func (r repo) missing(ctx context.Context, ids []string) ([]string, error) {
	ids = slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return id == "" })
	if len(ids) == 0 {
		return nil, nil
	}
	out, err := r.git(ctx, strings.NewReader(strings.Join(ids, "\n")+"\n"), r.gitDir, "", "cat-file", "--batch-check=%(objectname)")
	if err != nil {
		return nil, err
	}
	var lacking []string
	for line := range strings.Lines(out) {
		if id, ok := strings.CutSuffix(strings.TrimSuffix(line, "\n"), " missing"); ok {
			lacking = append(lacking, id)
		}
	}
	return lacking, nil
}

// fetch adds to main the objects of the shadow's repository that wants reach and main lacks, leaving out what haves
// reach. It writes them as a pack, as git fetch would, without changing any ref.
func fetch(ctx context.Context, main, shadow repo, wants, haves []string) error {
	wants, err := main.missing(ctx, wants)
	if err != nil || len(wants) == 0 {
		return err
	}
	lacking, err := shadow.missing(ctx, haves) // as after git gc on the worker
	if err != nil {
		return err
	}
	var input strings.Builder
	for _, id := range wants {
		input.WriteString(id + "\n")
	}
	for _, id := range haves {
		if id != "" && !slices.Contains(lacking, id) {
			input.WriteString("^" + id + "\n")
		}
	}
	pack := exec.CommandContext(ctx, "git", "--git-dir="+shadow.path(shadow.gitDir), "pack-objects", "--revs", "--stdout", "-q")
	pack.Stdin = strings.NewReader(input.String())
	var packErr bytes.Buffer
	pack.Stderr = &packErr
	out, err := pack.StdoutPipe()
	if err != nil {
		return err
	}
	if err := pack.Start(); err != nil {
		return err
	}
	_, err = main.git(ctx, out, main.gitDir, "", "index-pack", "--stdin")
	out.Close() // so that pack-objects stops if index-pack failed
	if waitErr := pack.Wait(); waitErr != nil && err == nil {
		return fmt.Errorf("git pack-objects in the shadow: %w: %s", waitErr, bytes.TrimSpace(packErr.Bytes()))
	}
	return err
}

// pin keeps objects from git gc with the ref name: a commit whose tree lists the blobs and whose parents are the
// commits, and refs next to it for the rest, such as annotated tags.
func (r repo) pin(ctx context.Context, name string, blobIDs, commits, others []string) error {
	var tree strings.Builder
	slices.Sort(blobIDs)
	for _, id := range slices.Compact(blobIDs) {
		fmt.Fprintf(&tree, "100644 blob %s\t%s\x00", id, id)
	}
	treeID, err := r.git(ctx, strings.NewReader(tree.String()), r.gitDir, "", "mktree", "-z")
	if err != nil {
		return err
	}
	commit := "tree " + strings.TrimSpace(treeID) + "\n"
	slices.Sort(commits)
	for _, id := range slices.Compact(commits) {
		if id != "" {
			commit += "parent " + id + "\n"
		}
	}
	commit += "author Gripi <gripi@localhost> 0 +0000\ncommitter Gripi <gripi@localhost> 0 +0000\n\nKept by Gripi for a session's move to a worker\n"
	id, err := r.git(ctx, strings.NewReader(commit), r.gitDir, "", "hash-object", "-t", "commit", "-w", "--stdin")
	if err != nil {
		return err
	}
	updates := "update " + name + " " + strings.TrimSpace(id) + "\n"
	slices.Sort(others)
	for _, other := range slices.Compact(others) {
		updates += "update " + path.Join(path.Dir(name), "objects", other) + " " + other + "\n"
	}
	_, err = r.git(ctx, strings.NewReader(updates), r.gitDir, "", "update-ref", "--stdin")
	return err
}
