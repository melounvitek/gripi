package mirror

import (
	"bufio"
	"errors"
	"io"
	"os/exec"
	"path"
	"slices"
	"strings"
)

// maxIgnoredSize is the largest Git-ignored file that is still mirrored, such
// as .env or config/master.key.
const maxIgnoredSize = 10 << 20

// Options adjust what is mirrored. Patterns are matched with path.Match
// against a path relative to its worktree and against each folder above it,
// so "vendor/cache" or "*.sqlite3" match at the top only and "config/*.key"
// in config.
type Options struct {
	Include []string // ignored paths to mirror anyway, at any size
	Exclude []string // paths never mirrored; wins over Include
}

// includesBelow reports whether a pattern can match something inside the folder rel.
func includesBelow(patterns []string, rel string) bool {
	dirs := strings.Split(rel, "/")
	for _, pattern := range patterns {
		parts := strings.Split(pattern, "/")
		if len(parts) <= len(dirs) {
			continue
		}
		matched := true
		for i, dir := range dirs {
			if ok, _ := path.Match(parts[i], dir); !ok {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func matchAny(patterns []string, rel string) bool {
	for p := rel; p != "." && p != "/"; p = path.Dir(p) {
		for _, pattern := range patterns {
			if ok, _ := path.Match(pattern, p); ok {
				return true
			}
		}
	}
	return false
}

// gitTemporary reports names Git uses only while an operation runs.
func gitTemporary(name string) bool {
	return strings.HasSuffix(name, ".lock") || strings.HasPrefix(name, "tmp_") ||
		strings.HasPrefix(name, ".tmp-") || strings.HasPrefix(name, "incoming-") || name == "gc.pid"
}

// classifier answers which paths of one worktree Git ignores, with a
// long-running git check-ignore.
type classifier struct {
	root    string
	cmd     *exec.Cmd
	in      io.WriteCloser
	out     *bufio.Reader
	ignore  map[string]bool
	tracked map[string]bool
	broken  bool // check-ignore failed again right after a restart, as in a deleted worktree
}

func newClassifier(root string) *classifier {
	c := &classifier{root: root}
	c.start()
	return c
}

// start (re)starts check-ignore, which reads the ignore files only once.
func (c *classifier) start() {
	c.stop()
	c.ignore, c.tracked, c.broken = map[string]bool{}, map[string]bool{}, false
	cmd := exec.Command("git", "-C", c.root, "check-ignore", "--stdin", "-z", "-v", "-n")
	in, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	if cmd.Start() != nil {
		return
	}
	c.cmd, c.in, c.out = cmd, in, bufio.NewReader(out)
}

func (c *classifier) stop() {
	if c.cmd == nil {
		return
	}
	c.in.Close()
	c.cmd.Process.Kill()
	c.cmd.Wait()
	c.cmd = nil
}

// ignored reports whether Git ignores rel, a path relative to the worktree;
// folders end with a slash, so that this also works for missing ones.
func (c *classifier) ignored(rel string) bool {
	if v, ok := c.ignore[rel]; ok || c.broken {
		return v
	}
	v, err := c.query(rel)
	if err != nil {
		c.start()
		if v, err = c.query(rel); err != nil {
			c.broken = true
			return false // mirroring too much is safer than too little
		}
	}
	c.ignore[rel] = v
	return v
}

func (c *classifier) query(rel string) (bool, error) {
	if c.cmd == nil {
		return false, errors.New("git check-ignore is not running")
	}
	if _, err := io.WriteString(c.in, rel+"\x00"); err != nil {
		return false, err
	}
	var fields [4]string // source, line number, pattern, path
	for i := range fields {
		s, err := c.out.ReadString(0)
		if err != nil {
			return false, err
		}
		fields[i] = strings.TrimSuffix(s, "\x00")
	}
	return fields[2] != "" && !strings.HasPrefix(fields[2], "!"), nil
}

// hasTracked reports whether Git tracks the file rel, or files in the folder
// rel: ignored paths are mirrored then, whatever their size.
func (c *classifier) hasTracked(rel string) bool {
	if v, ok := c.tracked[rel]; ok {
		return v
	}
	c.refreshTracked([]string{rel})
	return c.tracked[rel]
}

// refreshTracked asks Git which of the paths are tracked or contain tracked
// files, with one git call, and returns those.
func (c *classifier) refreshTracked(paths []string) []string {
	args := append([]string{"-C", c.root, "--literal-pathspecs", "ls-files", "-z", "--"}, paths...)
	out, err := exec.Command("git", args...).Output()
	files := strings.Split(string(out), "\x00")
	var found []string
	for _, p := range paths {
		// On error too: mirroring too much is safer.
		c.tracked[p] = err != nil || slices.ContainsFunc(files, func(f string) bool { return within(f, p) })
		if c.tracked[p] {
			found = append(found, p)
		}
	}
	return found
}

func (c *classifier) indexChanged() { c.tracked = map[string]bool{} }
