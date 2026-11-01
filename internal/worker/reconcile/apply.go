package reconcile

import (
	"errors"
	"fmt"
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
	// Only worktrees that the return makes, or whose checkouts it takes while main's HEAD and index there are still as
	// planned, may have a ref that it changes checked out.
	var taking []string
	for _, w := range p.Worktrees {
		taking = append(taking, w.Root)
	}
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
	return r.applyCheckouts()
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
	mRefs, err := r.main.refs(ctx)
	if err != nil {
		return err
	}
	mTrees, err := r.main.worktrees(ctx, mRefs)
	if err != nil {
		return err
	}
	if w.Head.Branch != "" && (mRefs[w.Head.Branch].OID != w.Head.OID || slices.ContainsFunc(mTrees, func(mw worktree) bool {
		return mw.Head.Branch == w.Head.Branch && mw.Root != w.Root
	})) {
		w.Head.Branch = "" // detached at the worker's commit, as main's branch differs now
	}
	i := slices.IndexFunc(mTrees, func(mw worktree) bool { return mw.Root == w.Root })
	if i < 0 {
		if e, err := lstat(w.Root); err != nil || e.Type != "" {
			return r.keepWorktree(w, "made on the worker, but main has something at its path")
		}
		args := []string{"worktree", "add", "--no-checkout", w.Root, strings.TrimPrefix(w.Head.Branch, "refs/heads/")}
		if w.Head.Branch == "" {
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
		i = slices.IndexFunc(mTrees, func(mw worktree) bool { return mw.Root == w.Root })
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
	worker, err := r.shadow.walk(ctx, w.Root, r.others(w.Root, r.plan.Worktrees), nil)
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
	if _, err := building.git(r.ctx, nil, gitDir, root, "update-index", "-q", "--refresh"); err != nil {
		return err
	}
	return os.Rename(built, live)
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
