package reconcile

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Item is something that main kept as it was, though the worker changed it too, or that it couldn't take.
type Item struct {
	Path   string `json:"path"` // a file, a worktree's folder, a ref, a file in the Git folder, or "stash"
	Reason string `json:"reason"`
	Ref    string `json:"ref,omitempty"` // the recovery ref that keeps the worker's commit
}

// Result is what a return did.
type Result struct {
	Applied []string `json:"applied,omitempty"` // main's paths that took the worker's version, and worktrees made
	Aside   []Item   `json:"aside,omitempty"`
	Refs    []string `json:"refs,omitempty"`   // the recovery refs
	Notice  string   `json:"notice,omitempty"` // for the session; empty when main kept nothing aside
}

// plan is a return's journal: every change to main, decided before the first, which happens only while main still
// holds what the plan expects, so that running the plan again finishes it.
type plan struct {
	Recover   []refUpdate  `json:"recover,omitempty"`
	Refs      []refUpdate  `json:"refs,omitempty"`
	Stashes   []stash      `json:"stashes,omitempty"` // oldest first
	Files     []fileUpdate `json:"files,omitempty"`
	Worktrees []worktree   `json:"worktrees,omitempty"` // made on the worker
	Checkouts []checkout   `json:"checkouts,omitempty"`
	Aside     []Item       `json:"aside,omitempty"`
}

type refUpdate struct {
	Name string `json:"name"`
	Old  string `json:"old,omitempty"`
	New  string `json:"new,omitempty"` // empty deletes
}

type fileUpdate struct {
	Root string `json:"root"`
	Path string `json:"path"`
	Base entry  `json:"base"`
	Old  entry  `json:"old"`
	New  entry  `json:"new"`
	From string `json:"from,omitempty"` // a file with New's contents
}

type checkout struct {
	Root     string `json:"root"`
	GitDir   string `json:"git_dir"`
	Old      head   `json:"old"` // once the plan's refs are applied
	New      head   `json:"new"`
	OldIndex string `json:"old_index"`
	NewIndex string `json:"new_index"`
}

// record names what Discard drops.
type record struct {
	Move   string `json:"move"`
	GitDir string `json:"git_dir"`
}

// interrupt, if set, runs after each change to main; an error stops Reconcile there, as a crash would.
var interrupt func() error

type reconciler struct {
	ctx          context.Context
	main, shadow repo
	base         manifest
	recovery     string
	plan         *plan
	applying     bool // past the plan, which no longer changes
	result       Result
}

// Reconcile brings what the worker changed, as the shadow has it, to main's folders and repository: each change that
// main didn't make too, a clean merge of text that both changed, and otherwise main's version, with the worker's kept
// in the recovery folder or under refs/gripi/returns/<move>/. Worktrees that weren't selected are left alone. Run
// again after a failure or crash, it finishes what it started; after it finished, it returns the same result. The
// repository's other sessions must be idle.
func Reconcile(ctx context.Context, base, shadow, recovery string) (Result, error) {
	var result Result
	if err := readJSON(filepath.Join(recovery, "result.json"), &result); !errors.Is(err, fs.ErrNotExist) {
		return result, err
	}
	r := &reconciler{ctx: ctx, recovery: recovery, plan: &plan{}}
	if err := readJSON(filepath.Join(base, "manifest.json"), &r.base); err != nil {
		return result, err
	}
	r.main, r.shadow = repo{gitDir: r.base.GitDir}, repo{gitDir: r.base.GitDir, at: shadow}
	if err := readJSON(r.in("plan.json"), r.plan); errors.Is(err, fs.ErrNotExist) {
		if err := writeJSON(r.in("record.json"), record{Move: r.base.Move, GitDir: r.base.GitDir}); err != nil {
			return result, err
		}
		if err := r.makePlan(); err != nil {
			return result, err
		}
		if err := syncFilesystems(recovery); err != nil {
			return result, err
		}
		if err := writeJSON(r.in("plan.json"), r.plan); err != nil {
			return result, err
		}
	} else if err != nil {
		return result, err
	}
	if err := r.apply(); err != nil {
		return result, err
	}
	r.result.Notice = notice(recovery, r.result.Aside)
	if err := syncFilesystems(append(r.roots(), r.base.GitDir, recovery)...); err != nil {
		return result, err
	}
	return r.result, writeJSON(r.in("result.json"), r.result)
}

// Discard drops the recovery record of a return, with the move's base and the refs that keep the worker's commits.
func Discard(ctx context.Context, recovery string) error {
	var rec record
	if err := readJSON(filepath.Join(recovery, "record.json"), &rec); errors.Is(err, fs.ErrNotExist) {
		return os.RemoveAll(recovery)
	} else if err != nil {
		return err
	}
	main := repo{gitDir: rec.GitDir}
	out, err := main.git(ctx, nil, rec.GitDir, "", "for-each-ref", "--format=delete %(refname)", "refs/gripi/moves/"+rec.Move+"/", "refs/gripi/returns/"+rec.Move+"/")
	if err != nil {
		return err
	}
	if _, err := main.git(ctx, strings.NewReader(out), rec.GitDir, "", "update-ref", "--stdin"); err != nil {
		return err
	}
	return os.RemoveAll(recovery)
}

func (r *reconciler) in(names ...string) string {
	return filepath.Join(append([]string{r.recovery}, names...)...)
}

func (r *reconciler) returnsRef(name string) string {
	return "refs/gripi/returns/" + r.base.Move + "/" + name
}

// roots are the folders of the worktrees that the return changes.
func (r *reconciler) roots() []string {
	var roots []string
	for _, w := range r.base.Worktrees {
		roots = append(roots, w.Root)
	}
	for _, w := range r.plan.Worktrees {
		roots = append(roots, w.Root)
	}
	return roots
}

func (r *reconciler) makePlan() error {
	ctx, b, p := r.ctx, &r.base, r.plan
	wRefs, err := r.shadow.refs(ctx)
	if err != nil {
		return err
	}
	wTrees, err := r.shadow.worktrees(ctx, wRefs)
	if err != nil {
		return err
	}
	wStash, err := r.shadow.stashes(ctx)
	if err != nil {
		return err
	}
	// The worker's worktrees that come back: the selected ones that it still has, and those it made.
	var back []worktree
	allRoots := slices.Clone(b.Registered)
	for _, w := range wTrees {
		selected := slices.ContainsFunc(b.Worktrees, func(bw worktree) bool { return bw.Root == w.Root })
		if _, err := os.Lstat(r.shadow.path(w.Root)); err != nil || !selected && slices.Contains(b.Registered, w.Root) {
			continue
		}
		if w.Index, err = r.shadow.index(ctx, w); err != nil {
			return err
		}
		back = append(back, w)
		allRoots = append(allRoots, w.Root)
	}

	// The worker's objects go to main first, kept from git gc until Discard.
	var wants, haves, indexBlobs, commits, others []string
	for _, rf := range wRefs {
		wants, commits = append(wants, rf.OID), append(commits, rf.Commit)
		if rf.OID != rf.Commit {
			others = append(others, rf.OID)
		}
	}
	for _, w := range back {
		wants, commits, indexBlobs = append(wants, w.Head.OID), append(commits, w.Head.OID), append(indexBlobs, blobs(w.Index)...)
	}
	for _, s := range wStash {
		wants, commits = append(wants, s.OID), append(commits, s.OID)
	}
	for _, rf := range b.Refs {
		haves = append(haves, rf.OID)
	}
	for _, w := range b.Worktrees {
		haves = append(haves, w.Head.OID)
	}
	for _, s := range b.Stash {
		haves = append(haves, s.OID)
	}
	if err := fetch(ctx, r.main, r.shadow, append(wants, indexBlobs...), haves); err != nil {
		return err
	}
	if err := r.main.pin(ctx, r.returnsRef("worker"), indexBlobs, commits, others); err != nil {
		return err
	}
	// Main keeps its reflogs; the worker's are only recorded.
	for _, dir := range append([]string{b.GitDir}, gitDirs(back)...) {
		if _, err := os.Lstat(r.shadow.path(dir + "/logs")); err == nil {
			if err := keepTree(r.shadow.path(dir+"/logs"), r.in("worker", dir, "logs")); err != nil {
				return err
			}
		}
	}

	mRefs, err := r.main.refs(ctx)
	if err != nil {
		return err
	}
	mTrees, err := r.main.worktrees(ctx, mRefs)
	if err != nil {
		return err
	}
	mStash, err := r.main.stashes(ctx)
	if err != nil {
		return err
	}
	checkedOut := map[string]string{} // branch → the folder of the worktree on main that has it
	for _, w := range mTrees {
		if w.Head.Branch != "" {
			checkedOut[w.Head.Branch] = w.Root
		}
	}
	onMain := func(root string) (worktree, bool) {
		i := slices.IndexFunc(mTrees, func(w worktree) bool { return w.Root == root })
		if e, err := lstat(root); i < 0 || err != nil || e.Type != "d" {
			return worktree{}, false
		}
		return mTrees[i], true
	}

	// A selected worktree takes the worker's HEAD and index only if main's checkout there is still the base's.
	taken := map[string]bool{}
	for _, bw := range b.Worktrees {
		i := slices.IndexFunc(back, func(w worktree) bool { return w.Root == bw.Root })
		if i < 0 {
			p.Aside = append(p.Aside, Item{Path: bw.Root, Reason: "the worker removed this worktree; main keeps it"})
			continue
		}
		w := back[i]
		if w.Head == bw.Head && w.Index == bw.Index {
			continue
		}
		mw, ok := onMain(bw.Root)
		if ok = ok && mw.Head == bw.Head; ok {
			if mw.Index, err = r.main.index(ctx, mw); err != nil {
				return err
			}
			ok = mw.Index == bw.Index
		}
		if ok {
			if ok, err = r.clean(bw, mw); err != nil {
				return err
			}
		}
		if branch := w.Head.Branch; ok && branch != "" {
			at, out := checkedOut[branch]
			ok = (!out || at == bw.Root) && (mRefs[branch].OID == wRefs[branch].OID || mRefs[branch].OID == b.Refs[branch].OID)
		}
		if ok {
			taken[bw.Root] = true
			p.Checkouts = append(p.Checkouts, checkout{Root: bw.Root, GitDir: mw.GitDir, Old: mw.Head, New: w.Head, OldIndex: mw.Index, NewIndex: w.Index})
			continue
		}
		if err := r.keepCheckout(w, "main's checkout there changed meanwhile, so it keeps its own HEAD and index"); err != nil {
			return err
		}
	}

	// Refs that both changed, or that a worktree on main has checked out and keeps, stay main's.
	for _, name := range slices.Sorted(maps.Keys(mergeKeys(b.Refs, wRefs))) {
		bo, wo, mo := b.Refs[name].OID, wRefs[name].OID, mRefs[name].OID
		at, out := checkedOut[name]
		switch {
		case wo == bo || mo == wo:
		case out && !taken[at]:
			err = r.recover(name, wo, "checked out on main in "+at+", which keeps its own")
		case mo == bo:
			p.Refs = append(p.Refs, refUpdate{Name: name, Old: mo, New: wo})
		case wo == "":
			err = r.recover(name, wo, "deleted on the worker, changed on main")
		default:
			err = r.recover(name, wo, "changed on both sides")
		}
		if err != nil {
			return err
		}
	}
	for i, c := range p.Checkouts {
		if c.Old.Branch != "" {
			p.Checkouts[i].Old.OID = r.refAfter(mRefs, c.Old.Branch)
		}
	}

	for i := len(wStash) - 1; i >= 0; i-- {
		if !slices.ContainsFunc(b.Stash, func(s stash) bool { return s.OID == wStash[i].OID }) {
			p.Stashes = append(p.Stashes, wStash[i])
		}
	}
	for _, s := range b.Stash {
		if !slices.Contains(wStash, s) && slices.Contains(mStash, s) {
			p.Aside = append(p.Aside, Item{Path: "stash", Reason: fmt.Sprintf("the worker dropped %q, which main keeps", s.Message)})
		}
	}

	// Worktrees made on the worker.
	for _, w := range back {
		if slices.Contains(b.Registered, w.Root) {
			continue
		}
		if e, err := lstat(w.Root); err != nil || e.Type != "" || slices.ContainsFunc(mTrees, func(mw worktree) bool { return mw.Root == w.Root }) {
			if err := r.keepWorktree(w, "made on the worker, but main has something at its path"); err != nil {
				return err
			}
			continue
		}
		h := w.Head
		if _, out := checkedOut[h.Branch]; out || !strings.HasPrefix(h.Branch, "refs/heads/") || r.refAfter(mRefs, h.Branch) != h.OID {
			h.Branch = "" // detached at the worker's commit
		} else {
			checkedOut[h.Branch] = w.Root
		}
		w.Head = h
		p.Worktrees = append(p.Worktrees, w)
	}

	var made []string // worktrees made on main meanwhile, which the worker's files stay out of
	for _, mw := range mTrees {
		if !slices.Contains(b.Registered, mw.Root) {
			made = append(made, mw.Root)
		}
	}
	for _, bw := range b.Worktrees {
		if slices.ContainsFunc(back, func(w worktree) bool { return w.Root == bw.Root }) {
			_, ok := onMain(bw.Root)
			if err := r.planFiles(bw.Root, allRoots, made, ok); err != nil {
				return err
			}
		}
	}
	return r.planGitFiles()
}

func gitDirs(worktrees []worktree) []string {
	var dirs []string
	for _, w := range worktrees {
		dirs = append(dirs, w.GitDir)
	}
	return dirs
}

func mergeKeys[V any](a, b map[string]V) map[string]bool {
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	return keys
}

// refAfter is what main's ref will be once the plan's refs are applied.
func (r *reconciler) refAfter(mRefs map[string]ref, name string) string {
	if i := slices.IndexFunc(r.plan.Refs, func(u refUpdate) bool { return u.Name == name }); i >= 0 {
		return r.plan.Refs[i].New
	}
	return mRefs[name].OID
}

// clean reports whether the files of main's worktree that Git doesn't ignore still are the base's.
func (r *reconciler) clean(bw, mw worktree) (bool, error) {
	now, err := dirty(r.ctx, mw)
	if err != nil {
		return false, err
	}
	slices.Sort(now)
	if !slices.Equal(now, slices.Sorted(slices.Values(bw.Dirty))) {
		return false, nil
	}
	var paths []string
	for _, p := range now {
		if !strings.HasSuffix(p, "/") { // a repository in the worktree
			paths = append(paths, filepath.Join(bw.Root, p))
		}
	}
	found, err := r.main.entries(r.ctx, bw.Root, paths)
	if err != nil {
		return false, err
	}
	for _, p := range paths {
		if !found[p].same(r.base.Files[p]) {
			return false, nil
		}
	}
	return true, nil
}

// kept notes something that main kept as it was. With a ref, a recovery ref, it keeps the worker's commit oid there.
// While planning, the plan notes them; once it is applied, they're applied at once.
func (r *reconciler) kept(item Item, oid string) error {
	if !r.applying {
		r.plan.Aside = append(r.plan.Aside, item)
		if item.Ref != "" {
			r.plan.Recover = append(r.plan.Recover, refUpdate{Name: item.Ref, New: oid})
		}
		return nil
	}
	r.result.Aside = append(r.result.Aside, item)
	if item.Ref == "" {
		return nil
	}
	r.result.Refs = append(r.result.Refs, item.Ref)
	return r.updateRefs([]refUpdate{{Name: item.Ref, New: oid}}, false)
}

// recover keeps the worker's version of the ref name, which main didn't take.
func (r *reconciler) recover(name, oid, reason string) error {
	item := Item{Path: name, Reason: reason}
	if oid != "" {
		item.Ref = r.returnsRef(name)
	}
	return r.kept(item, oid)
}

// keepCheckout keeps the worker's HEAD and index of a worktree, which main didn't take: the index as a list, and a
// detached HEAD under a recovery ref; a branch has its own.
func (r *reconciler) keepCheckout(w worktree, reason string) error {
	list := r.in("worker", w.GitDir, "index.txt")
	if err := os.MkdirAll(filepath.Dir(list), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(list, []byte(strings.ReplaceAll(w.Index, "\x00", "\n")), 0o600); err != nil {
		return err
	}
	item := Item{Path: w.Root, Reason: reason + "; the worker's index is listed in " + list}
	if w.Head.Branch == "" && w.Head.OID != "" {
		item.Ref = r.returnsRef("worktrees/" + filepath.Base(w.GitDir) + "/HEAD")
	}
	return r.kept(item, w.Head.OID)
}

// keepWorktree keeps a worktree that the worker made, with its files, HEAD and index, which main didn't take.
func (r *reconciler) keepWorktree(w worktree, reason string) error {
	if err := keepTree(r.shadow.path(w.Root), r.in("worker", w.Root)); err != nil {
		return err
	}
	return r.keepCheckout(w, reason+"; its files are in "+r.in("worker", w.Root))
}

// planFiles plans the files of a selected worktree that the worker changed.
func (r *reconciler) planFiles(root string, allRoots, made []string, onMain bool) error {
	others := slices.DeleteFunc(slices.Clone(allRoots), func(other string) bool { return other == root })
	owned := func(p string) bool {
		return within(p, root) && !slices.ContainsFunc(others, func(other string) bool { return within(p, other) && len(other) > len(root) })
	}
	base := map[string]entry{}
	for p, e := range r.base.Files {
		if owned(p) {
			base[p] = e
		}
	}
	worker, err := r.shadow.walk(r.ctx, root, others, base)
	if err != nil {
		return err
	}
	var changed []string
	for p := range mergeKeys(base, worker) {
		if !base[p].same(worker[p]) {
			changed = append(changed, p)
		}
	}
	slices.Sort(changed)
	if !onMain {
		for _, p := range changed {
			if err := r.aside(p, base[p], entry{}, worker[p], "its worktree is gone on main"); err != nil {
				return err
			}
		}
		return nil
	}
	main, err := r.main.entries(r.ctx, root, changed)
	if err != nil {
		return err
	}
	for _, p := range changed {
		b, m, w := base[p], main[p], worker[p]
		if m.same(w) {
			continue
		}
		u := fileUpdate{Root: root, Path: p, Base: b, Old: m, New: w, From: r.shadow.path(p)}
		reason := ""
		switch {
		case slices.ContainsFunc(made, func(other string) bool { return within(p, other) }):
			reason = "in a worktree that main made there meanwhile"
		case m.same(b):
		case b.Type == "f" && m.Type == "f" && w.Type == "f":
			if u.New, u.From, err = r.merge(p, b, m, w); err != nil {
				return err
			}
			if u.From == "" {
				reason = clash(b, m, w)
			}
		default:
			reason = clash(b, m, w)
		}
		if reason == "" {
			r.plan.Files = append(r.plan.Files, u)
		} else if err := r.aside(p, b, m, w, reason); err != nil {
			return err
		}
	}
	return nil
}

func clash(b, m, w entry) string {
	switch {
	case b.Type == "":
		return "added on both sides"
	case w.Type == "":
		return "deleted on the worker, changed on main"
	case m.Type == "":
		return "deleted on main, changed on the worker"
	}
	return "changed on both sides"
}

// merge merges the worker's and main's changes to the text file p with git merge-file into a file in the recovery
// folder, which it returns, or "" if they clash.
func (r *reconciler) merge(p string, b, m, w entry) (entry, string, error) {
	mode := m.Mode
	if m.Mode == b.Mode {
		mode = w.Mode
	} else if w.Mode != b.Mode && w.Mode != m.Mode {
		return entry{}, "", nil
	}
	n := len(r.plan.Files)
	baseFile, merged := r.in("merged", fmt.Sprint(n, ".base")), r.in("merged", fmt.Sprint(n))
	if err := r.main.keep(r.ctx, baseFile, b, ""); err != nil {
		return entry{}, "", err
	}
	defer os.Remove(baseFile)
	out, err := r.main.git(r.ctx, nil, r.base.GitDir, "", "merge-file", "-p", p, baseFile, r.shadow.path(p))
	if err != nil { // a conflict, or a binary file
		return entry{}, "", nil
	}
	if err := os.WriteFile(merged, []byte(out), fs.FileMode(mode)); err != nil {
		return entry{}, "", err
	}
	ids, err := r.main.hash(r.ctx, []string{merged}, false)
	if err != nil {
		return entry{}, "", err
	}
	return entry{Type: "f", Mode: mode, OID: ids[0]}, merged, nil
}

// aside keeps the base's, main's and the worker's versions of p in the recovery folder.
func (r *reconciler) aside(p string, b, m, w entry, reason string) error {
	if err := r.main.keep(r.ctx, r.in("base", p), b, ""); err != nil {
		return err
	}
	if err := r.main.keep(r.ctx, r.in("main", p), m, p); err != nil {
		return err
	}
	if err := r.main.keep(r.ctx, r.in("worker", p), w, r.shadow.path(p)); err != nil {
		return err
	}
	return r.kept(Item{Path: p, Reason: reason}, "")
}

// planGitFiles keeps the worker's changes to the repository's config and hooks aside: main keeps its own.
func (r *reconciler) planGitFiles() error {
	hooks := filepath.Join(r.base.GitDir, "hooks")
	worker, err := r.shadow.walk(r.ctx, hooks, nil, r.base.GitFiles)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	config := filepath.Join(r.base.GitDir, "config")
	found, err := r.shadow.entries(r.ctx, r.base.GitDir, []string{config})
	if err != nil {
		return err
	}
	if worker == nil {
		worker = map[string]entry{}
	}
	worker[config] = found[config]
	for _, p := range slices.Sorted(maps.Keys(mergeKeys(r.base.GitFiles, worker))) {
		if w := worker[p]; w.Type != "d" && !w.same(r.base.GitFiles[p]) {
			if err := r.main.keep(r.ctx, r.in("worker", p), w, r.shadow.path(p)); err != nil {
				return err
			}
			if err := r.kept(Item{Path: p, Reason: "changed on the worker; main keeps its own"}, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func notice(recovery string, aside []Item) string {
	if len(aside) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Main kept its own version of what follows. The worker's is in %s or under the ref named, with the base's and main's in %s and %s to compare, until the record is discarded:",
		filepath.Join(recovery, "worker"), filepath.Join(recovery, "base"), filepath.Join(recovery, "main"))
	for _, item := range aside {
		fmt.Fprintf(&b, "\n- %s: %s", item.Path, item.Reason)
		if item.Ref != "" {
			fmt.Fprintf(&b, "; the worker's commit is %s", item.Ref)
		}
	}
	return b.String()
}
