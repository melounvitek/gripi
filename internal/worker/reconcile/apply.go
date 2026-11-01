package reconcile

import (
	"errors"
	"fmt"
	"maps"
	"os"
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
	var updates []refUpdate
	for _, u := range p.Refs {
		switch mRefs[u.Name].OID {
		case u.New:
		case u.Old:
			updates = append(updates, u)
		default:
			if err := r.recover(u.Name, u.New, "changed on main meanwhile"); err != nil {
				return err
			}
		}
	}
	if len(updates) > 0 {
		if err := r.updateRefs(updates, true); err != nil {
			return err
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
	now := map[string]entry{}
	for root, paths := range byRoot {
		found, err := r.main.entries(r.ctx, root, paths)
		if err != nil {
			return err
		}
		maps.Copy(now, found)
	}
	for _, u := range updates {
		switch current := now[u.Path]; {
		case current.same(u.New):
			r.result.Applied = append(r.result.Applied, u.Path)
		case current.same(u.Old):
			var err error
			if u.New.Type == "" {
				err = os.Remove(u.Path)
			} else {
				err = place(u.Path, u.New, u.From)
			}
			switch {
			case err == nil:
				r.result.Applied = append(r.result.Applied, u.Path)
			case u.New.Type == "" && errors.Is(err, syscall.ENOTEMPTY):
				// a folder that main keeps things in
			case errors.Is(err, syscall.ENOTEMPTY):
				if err := r.asideNow(u, current, "main keeps files in a folder where the worker has a file"); err != nil {
					return err
				}
			default:
				return err
			}
		default:
			if err := r.asideNow(u, current, "changed on main meanwhile"); err != nil {
				return err
			}
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
// index.
func (r *reconciler) applyWorktree(w newWorktree) error {
	ctx := r.ctx
	mTrees, err := r.main.worktrees(ctx, nil)
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(mTrees, func(mw worktree) bool { return mw.Root == w.Root }) {
		if e, err := lstat(w.Root); err != nil || e.Type != "" {
			return r.keepWorktree(worktree{Root: w.Root, GitDir: w.GitDir, Head: w.Head, Index: w.Index}, "made on the worker, but main has something at its path")
		}
		args := []string{"worktree", "add", "--no-checkout"}
		if w.Head.Branch != "" {
			args = append(args, w.Root, strings.TrimPrefix(w.Head.Branch, "refs/heads/"))
		} else {
			args = append(args, "--detach", w.Root, w.Head.OID)
		}
		if _, err := r.main.git(ctx, nil, r.base.GitDir, "", args...); err != nil {
			return err
		}
		if mTrees, err = r.main.worktrees(ctx, nil); err != nil {
			return err
		}
	}
	i := slices.IndexFunc(mTrees, func(mw worktree) bool { return mw.Root == w.Root })
	worker, err := r.shadow.walk(ctx, w.Root, nil, nil)
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
		case m.Type == "":
			if err := place(p, worker[p], r.shadow.path(p)); err != nil {
				return err
			}
		default:
			if err := r.asideNow(fileUpdate{Path: p}, m, "changed on main meanwhile"); err != nil {
				return err
			}
		}
	}
	if err := r.setIndex(mTrees[i].GitDir, w.Root, w.Index); err != nil {
		return err
	}
	r.result.Applied = append(r.result.Applied, w.Root)
	return r.step()
}

func (r *reconciler) setIndex(gitDir, root, index string) error {
	if _, err := r.main.git(r.ctx, nil, gitDir, root, "read-tree", "--empty"); err != nil {
		return err
	}
	if _, err := r.main.git(r.ctx, strings.NewReader(index), gitDir, root, "update-index", "-z", "--index-info"); err != nil {
		return err
	}
	_, err := r.main.git(r.ctx, nil, gitDir, root, "update-index", "-q", "--refresh")
	return err
}

// applyCheckouts gives selected worktrees the worker's HEAD and index.
func (r *reconciler) applyCheckouts() error {
	mRefs, err := r.main.refs(r.ctx)
	if err != nil {
		return err
	}
	sameHead := func(a, b head) bool { return a.Branch == b.Branch && (a.Branch != "" || a.OID == b.OID) }
	for _, c := range r.plan.Checkouts {
		w := worktree{Root: c.Root, GitDir: c.GitDir}
		h, err := r.main.head(r.ctx, c.GitDir, mRefs)
		if err != nil {
			return err
		}
		index, err := r.main.index(r.ctx, w)
		if err != nil {
			return err
		}
		if !sameHead(h, c.Old) && !sameHead(h, c.New) || index != c.OldIndex && index != c.NewIndex {
			w.Head, w.Index = c.New, c.NewIndex
			if err := r.keepCheckout(w, "main's checkout there changed meanwhile, so it keeps its own HEAD and index"); err != nil {
				return err
			}
			continue
		}
		if !sameHead(h, c.New) {
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
