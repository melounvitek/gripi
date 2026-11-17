package reconcile

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
)

// step marks a change to main.
func (r *reconciler) step() error {
	if interrupt != nil {
		if err := interrupt(); err != nil {
			return err
		}
	}
	return r.ctx.Err()
}

func (r *reconciler) apply() error {
	ctx, p := r.ctx, r.plan
	r.applying = true
	r.result = Result{Aside: slices.Clone(p.Aside)}
	for _, u := range p.Recover {
		r.result.Refs = append(r.result.Refs, u.Name)
	}
	if len(p.Recover) > 0 {
		if err := r.updateRefs(p.Recover, false); err != nil {
			return err
		}
		if err := r.step(); err != nil {
			return err
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
	// Only worktrees whose checkouts the return takes while main's HEAD and index there are still as planned may have a
	// ref that it changes checked out. Those it makes come after the refs.
	var taking []string
	for _, c := range p.Checkouts {
		i := slices.IndexFunc(mTrees, func(w worktree) bool { return w.Root == c.Root && w.GitDir == c.GitDir })
		if i < 0 || mTrees[i].Head.Branch != c.Old.Branch {
			continue
		}
		if index, err := r.main.index(ctx, mTrees[i]); err != nil {
			return err
		} else if index == c.OldIndex {
			taking = append(taking, c.Root)
		}
	}
	var updates []refUpdate
	for _, u := range p.Refs {
		checkedOut := slices.ContainsFunc(mTrees, func(w worktree) bool { return w.Head.Branch == u.Name && !slices.Contains(taking, w.Root) })
		switch current := mRefs[u.Name].OID; {
		case current == u.New:
		case r.failure != "":
			if err := r.recover(u.Name, u.New, notBack); err != nil {
				return err
			}
		case current == u.Old && !checkedOut:
			updates = append(updates, u)
		default:
			if err := r.recover(u.Name, u.New, "changed or checked out on main meanwhile"); err != nil {
				return err
			}
		}
	}
	if len(updates) > 0 {
		// A ref in the way, such as main's x for the worker's x/y, fails them all.
		if err := r.updateRefs(updates, true); err != nil {
			for _, u := range updates {
				if r.updateRefs([]refUpdate{u}, true) != nil {
					if err := r.recover(u.Name, u.New, "main has a ref in its way"); err != nil {
						return err
					}
				}
			}
		}
		if err := r.step(); err != nil {
			return err
		}
	}

	mStash, err := r.main.stashes(ctx)
	if err != nil {
		return err
	}
	for _, s := range p.Stashes {
		if slices.ContainsFunc(mStash, func(m stash) bool { return m.OID == s.OID }) {
			continue
		}
		if r.failure != "" {
			if err := r.kept(Item{Path: "stash", Reason: fmt.Sprintf("the worker's %q, %s", s.Message, notBack), Ref: r.returnsRef("stashes/" + s.OID)}, s.OID); err != nil {
				return err
			}
			continue
		}
		if _, err := r.main.git(ctx, nil, r.base.GitDir, "", "stash", "store", "-m", s.Message, s.OID); err != nil {
			return err
		}
		if err := r.step(); err != nil {
			return err
		}
	}

	if err := r.applyFiles(); err != nil {
		return err
	}
	for _, w := range p.Worktrees {
		if err := r.applyWorktree(w); err != nil {
			return err
		}
	}
	if err := r.applyCheckouts(); err != nil {
		return err
	}
	return r.applyReflogs()
}

// updateRefs applies updates in one transaction; with check, only to refs that still hold their old values.
func (r *reconciler) updateRefs(updates []refUpdate, check bool) error {
	var input strings.Builder
	for _, u := range updates {
		old := ""
		if check {
			old = u.Old
		}
		switch {
		case u.New == "":
			fmt.Fprintf(&input, "delete %s\x00%s\x00", u.Name, old)
		case check && u.Old == "":
			fmt.Fprintf(&input, "create %s\x00%s\x00", u.Name, u.New)
		default:
			fmt.Fprintf(&input, "update %s\x00%s\x00%s\x00", u.Name, u.New, old)
		}
	}
	_, err := r.main.git(r.ctx, strings.NewReader(input.String()), r.base.GitDir, "", "update-ref", "-m", "gripi: back from a worker", "--stdin", "-z")
	return err
}

// applyFiles applies the planned changes to files, removals deepest first, and the rest top down.
func (r *reconciler) applyFiles() error {
	updates := slices.Clone(r.plan.Files)
	slices.SortFunc(updates, func(a, b fileUpdate) int {
		if (a.New.Type == "") != (b.New.Type == "") {
			if a.New.Type == "" {
				return -1
			}
			return 1
		}
		if a.New.Type == "" {
			return strings.Compare(b.Path, a.Path)
		}
		return strings.Compare(a.Path, b.Path)
	})
	byRoot := map[string][]string{}
	for _, u := range updates {
		byRoot[u.Root] = append(byRoot[u.Root], u.Path)
	}
	mTrees, err := r.main.worktrees(r.ctx, nil)
	if err != nil {
		return err
	}
	made := r.madeOnMain(mTrees)
	now := map[string]entry{}
	for root, paths := range byRoot {
		found, err := r.main.entries(r.ctx, root, paths)
		if err != nil {
			return err
		}
		maps.Copy(now, found)
	}
	for _, u := range updates {
		current := now[u.Path]
		applied, err := current.same(u.New), error(nil)
		switch {
		case applied:
		case r.failure != "":
			err = r.asideNow(u, current, notBack)
		case !current.same(u.Old):
			err = r.asideNow(u, current, "changed on main meanwhile")
		case slices.ContainsFunc(made, func(root string) bool { return within(u.Path, root) }):
			err = r.asideNow(u, current, "in a worktree that main made there meanwhile")
		case blocked(u.Root, u.Path): // as when main kept a file or link where the worker has a folder
			err = r.asideNow(u, current, "something on main is in its way")
		case u.New.Type == "":
			err = os.Remove(u.Path)
			applied = err == nil
			if errors.Is(err, syscall.ENOTEMPTY) {
				err = nil // a folder that main keeps things in
			}
		default:
			err = place(u.Path, u.New, u.From)
			applied = err == nil
			if errors.Is(err, syscall.ENOTEMPTY) {
				err = r.asideNow(u, current, "main keeps files in a folder where the worker has a file")
			}
		}
		if err != nil {
			return err
		}
		if applied {
			r.result.Applied = append(r.result.Applied, u.Path)
		}
		if err := r.step(); err != nil {
			return err
		}
	}
	return nil
}

// asideNow keeps the worker's version of a file that main changed after the plan was made.
func (r *reconciler) asideNow(u fileUpdate, current entry, reason string) error {
	w, err := lstat(r.shadow.path(u.Path))
	if err != nil {
		return err
	}
	return r.aside(u.Path, u.Base, current, w, reason)
}

// applyWorktree makes a worktree that the worker made, with git worktree add, and gives it the worker's files and
// index. A worktree that main has at its path is the one it made only with its HEAD and an empty or the worker's
// index.
func (r *reconciler) applyWorktree(w worktree) error {
	ctx := r.ctx
	// Planned all the same, so that a worktree that it is in leaves its files out.
	if inProgress(r.shadow.path(w.GitDir)) {
		return r.keepWorktree(w, "made on the worker, which was in the middle of a merge, rebase, cherry-pick or revert there")
	}
	mRefs, err := r.main.refs(ctx)
	if err != nil {
		return err
	}
	mTrees, err := r.main.worktrees(ctx, mRefs)
	if err != nil {
		return err
	}
	if r.failure != "" {
		// One that the return made has the worker's HEAD.
		if i := slices.IndexFunc(mTrees, func(mw worktree) bool { return mw.Root == w.Root }); i >= 0 && mTrees[i].Head.OID == w.Head.OID {
			index, err := r.main.index(ctx, mTrees[i])
			if err != nil {
				return err
			}
			worker, err := r.shadow.walk(ctx, w.Root, r.others(w.Root, r.plan.Worktrees))
			if err != nil {
				return err
			}
			paths := slices.Collect(maps.Keys(worker))
			main, err := r.main.entries(ctx, w.Root, paths)
			if err != nil {
				return err
			}
			if index == w.Index && !slices.ContainsFunc(paths, func(p string) bool { return !main[p].same(worker[p]) }) {
				r.result.Applied = append(r.result.Applied, w.Root)
				return nil
			}
			if index == "" && w.Head.OID != "" {
				// An index of its HEAD makes them agree.
				if _, err := r.main.git(ctx, nil, mTrees[i].GitDir, "", "read-tree", "HEAD"); err != nil {
					return err
				}
				if err := r.step(); err != nil {
					return err
				}
			}
		}
		return r.keepWorktree(w, notBack)
	}
	if w.Head.Branch != "" && (mRefs[w.Head.Branch].OID != w.Head.OID || slices.ContainsFunc(mTrees, func(mw worktree) bool {
		return mw.Head.Branch == w.Head.Branch && mw.Root != w.Root
	})) {
		w.Head.Branch = "" // detached at the worker's commit, as main's branch differs now
	}
	i := slices.IndexFunc(mTrees, func(mw worktree) bool { return mw.Root == w.Root })
	if i < 0 {
		// Git would make the worktree where a link above its path leads, and register it there.
		if e, err := lstat(w.Root); err != nil || e.Type != "" || blocked("/", w.Root) {
			return r.keepWorktree(w, "made on the worker, but main has something at its path")
		}
		name := strings.TrimPrefix(w.Head.Branch, "refs/heads/")
		args := []string{"worktree", "add", "--no-checkout", w.Root, name}
		switch {
		case w.Head.OID == "" && w.Head.Branch == "":
			return r.keepWorktree(w, "made on the worker on a branch without commits, whose name main has taken meanwhile")
		case w.Head.OID == "": // it checks nothing out, and refuses --no-checkout
			args = []string{"worktree", "add", "--orphan", "-b", name, w.Root}
		case w.Head.Branch == "":
			args = []string{"worktree", "add", "--no-checkout", "--detach", w.Root, w.Head.OID}
		}
		if _, err := r.main.git(ctx, nil, r.base.GitDir, "", args...); err != nil {
			return err
		}
		if err := r.step(); err != nil {
			return err
		}
		if mTrees, err = r.main.worktrees(ctx, mRefs); err != nil {
			return err
		}
		if i = slices.IndexFunc(mTrees, func(mw worktree) bool { return mw.Root == w.Root }); i < 0 {
			return r.keepWorktree(w, "made on the worker, but Git on main registered it at another path")
		}
	}
	mw, made := mTrees[i], r.madeOnMain(mTrees)
	if e, err := lstat(w.Root); err != nil || e.Type != "d" {
		return r.keepWorktree(w, "made on the worker, but main has something else at its path")
	}
	index, err := r.main.index(ctx, mw)
	if err != nil {
		return err
	}
	if mw.Head != w.Head || index != "" && index != w.Index {
		return r.keepWorktree(w, "made on the worker, but main has a worktree of its own there")
	}
	worker, err := r.shadow.walk(ctx, w.Root, r.others(w.Root, r.plan.Worktrees))
	if err != nil {
		return err
	}
	paths := slices.Sorted(maps.Keys(worker))
	main, err := r.main.entries(ctx, w.Root, paths)
	if err != nil {
		return err
	}
	for _, p := range paths {
		switch m := main[p]; {
		case m.same(worker[p]):
		case m.Type != "":
			err = r.asideNow(fileUpdate{Path: p}, m, "changed on main meanwhile")
		case slices.ContainsFunc(made, func(root string) bool { return within(p, root) }):
			err = r.asideNow(fileUpdate{Path: p}, m, "in a worktree that main made there meanwhile")
		case blocked(w.Root, p):
			err = r.asideNow(fileUpdate{Path: p}, m, "something on main is in its way")
		default:
			err = place(p, worker[p], r.shadow.path(p))
		}
		if err != nil {
			return err
		}
	}
	if index != w.Index {
		if err := r.setIndex(mw.GitDir, w.Root, w.Index); err != nil {
			return err
		}
	}
	r.result.Applied = append(r.result.Applied, w.Root)
	return r.step()
}

// setIndex gives a worktree on main the index that Git builds from a list, in one rename, so that it's never half
// built.
func (r *reconciler) setIndex(gitDir, root, index string) error {
	live := filepath.Join(gitDir, "index")
	if _, err := os.Lstat(live + ".lock"); err == nil {
		return fmt.Errorf("git is changing %s", live)
	}
	built := live + ".gripi-" + randomName() // not one that a stopped attempt left locked
	defer os.Remove(built)
	building := repo{gitDir: r.base.GitDir, env: []string{"GIT_INDEX_FILE=" + built}}
	if _, err := building.git(r.ctx, strings.NewReader(index), gitDir, root, "update-index", "-z", "--index-info"); err != nil {
		return err
	}
	if _, err := building.git(r.ctx, nil, gitDir, root, "update-index", "-q", "--unmerged", "--refresh"); err != nil {
		return err
	}
	return os.Rename(built, live)
}

// applyReflogs gives main's reflogs of the refs and HEADs that main has as the worker left them the worker's entries,
// so that Git keeps the commits that the session left there, such as with git reset. Main's other reflogs would
// contradict main's refs with them.
func (r *reconciler) applyReflogs() error {
	mTrees, err := r.main.worktrees(r.ctx, nil)
	if err != nil {
		return err
	}
	for _, l := range r.plan.Reflogs {
		mRefs, err := r.main.refs(r.ctx) // anew, as Git may have changed them since the last
		if err != nil {
			return err
		}
		gitDir, now := r.base.GitDir, head{OID: mRefs[l.Ref].OID}
		if l.Root != "" {
			i := slices.IndexFunc(mTrees, func(w worktree) bool { return w.Root == l.Root })
			if i < 0 {
				continue
			}
			gitDir = mTrees[i].GitDir
			if now, err = r.main.head(r.ctx, gitDir, mRefs); err != nil {
				return err
			}
		}
		if now != l.Head || now.OID == "" {
			continue
		}
		if r.failure != "" { // the recovery folder has the worker's reflogs already
			log := filepath.Join(gitDir, "logs", l.Ref)
			if lacking, _, err := lackingEntries(log, l.Entries); err != nil || lacking != "" {
				if err := r.kept(Item{Path: log, Reason: notBack}, ""); err != nil {
					return err
				}
			}
			continue
		}
		if _, err := os.Lstat(filepath.Join(gitDir, l.Ref+".lock")); err == nil {
			return fmt.Errorf("git is changing %s in %s", l.Ref, gitDir)
		}
		if err := appendEntries(filepath.Join(gitDir, "logs", l.Ref), l.Entries); err != nil {
			return err
		}
		if err := r.step(); err != nil {
			return err
		}
	}
	return nil
}

// lackingEntries returns the entries that the reflog log lacks, and what it holds.
func lackingEntries(log, entries string) (string, []byte, error) {
	data, err := os.ReadFile(log)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", nil, err
	}
	has := map[string]bool{}
	for line := range strings.Lines(string(data)) {
		has[line] = true
	}
	var lacking strings.Builder
	for line := range strings.Lines(entries) {
		if !has[line] {
			lacking.WriteString(line)
		}
	}
	return lacking.String(), data, nil
}

// appendEntries appends to the reflog log the entries that it lacks.
func appendEntries(log, entries string) error {
	text, data, err := lackingEntries(log, entries)
	if err != nil || text == "" {
		return err
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		text = "\n" + text // after a line that a crash cut short
	}
	if err := os.MkdirAll(filepath.Dir(log), 0o777); err != nil {
		return err
	}
	f, err := os.OpenFile(log, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o666)
	if err != nil {
		return err
	}
	_, err = f.WriteString(text)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// applyCheckouts gives selected worktrees the worker's HEAD and index, while main has the HEAD and index that the plan
// expects there. Files that main changed after a stopped return was planned stay as changes against the worker's.
func (r *reconciler) applyCheckouts() error {
	mRefs, err := r.main.refs(r.ctx)
	if err != nil {
		return err
	}
	mTrees, err := r.main.worktrees(r.ctx, mRefs)
	if err != nil {
		return err
	}
	for _, c := range r.plan.Checkouts {
		i := slices.IndexFunc(mTrees, func(mw worktree) bool { return mw.Root == c.Root && mw.GitDir == c.GitDir })
		elsewhere := slices.ContainsFunc(mTrees, func(mw worktree) bool {
			return c.New.Branch != "" && mw.Head.Branch == c.New.Branch && mw.Root != c.Root
		})
		w := worktree{Root: c.Root, GitDir: c.GitDir}
		e, _ := lstat(c.Root)
		gone := i < 0 || e.Type != "d"
		var index string
		if !gone {
			if index, err = r.main.index(r.ctx, w); err != nil {
				return err
			}
		}
		if r.failure != "" {
			if !gone && mTrees[i].Head == c.New && index == c.NewIndex {
				continue
			}
			if !gone && index == c.OldIndex {
				if err := r.undoCheckout(c, mTrees[i].Head, mRefs); err != nil {
					return err
				}
			}
			w.Head, w.Index = c.New, c.NewIndex
			if err := r.keepCheckout(w, notBack); err != nil {
				return err
			}
			continue
		}
		if gone || mTrees[i].Head != c.Old && mTrees[i].Head != c.New || index != c.OldIndex && index != c.NewIndex || elsewhere {
			w.Head, w.Index = c.New, c.NewIndex
			if err := r.keepCheckout(w, "main's checkout there changed meanwhile, so it keeps its own HEAD and index"); err != nil {
				return err
			}
			continue
		}
		if mTrees[i].Head != c.New {
			if c.New.Branch != "" {
				_, err = r.main.git(r.ctx, nil, c.GitDir, "", "symbolic-ref", "HEAD", c.New.Branch)
			} else {
				_, err = r.main.git(r.ctx, nil, c.GitDir, "", "update-ref", "--no-deref", "HEAD", c.New.OID)
			}
			if err != nil {
				return err
			}
		}
		if index != c.NewIndex {
			if err := r.setIndex(c.GitDir, c.Root, c.NewIndex); err != nil {
				return err
			}
		}
		if err := r.step(); err != nil {
			return err
		}
	}
	return nil
}

// undoCheckout moves main's HEAD in a worktree whose checkout a failure cut short, and the branch that it has there,
// back to as they were before the return, as its index still is, and keeps the worker's commit of the branch aside.
func (r *reconciler) undoCheckout(c checkout, now head, mRefs map[string]ref) error {
	if now == c.New && now != c.Old {
		var err error
		if c.Old.Branch != "" {
			_, err = r.main.git(r.ctx, nil, c.GitDir, "", "symbolic-ref", "HEAD", c.Old.Branch)
		} else {
			_, err = r.main.git(r.ctx, nil, c.GitDir, "", "update-ref", "--no-deref", "HEAD", c.Old.OID)
		}
		if err != nil {
			return err
		}
		if err := r.step(); err != nil {
			return err
		}
	} else if now.Branch != c.Old.Branch {
		return nil
	}
	i := slices.IndexFunc(r.plan.Refs, func(u refUpdate) bool { return u.Name == c.Old.Branch })
	if c.Old.Branch == "" || i < 0 || mRefs[c.Old.Branch].OID != r.plan.Refs[i].New {
		return nil
	}
	u := r.plan.Refs[i]
	if err := r.updateRefs([]refUpdate{{Name: u.Name, Old: u.New, New: u.Old}}, true); err != nil {
		return err
	}
	if err := r.recover(u.Name, u.New, notBack); err != nil {
		return err
	}
	return r.step()
}
