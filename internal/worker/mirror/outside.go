package mirror

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// WriteOutside writes a tar archive of what the folders of unit.Within hold outside the unit's folders, also those of
// worktrees that joined it, which a mirror of the unit never sends: folders with what they hold, files and symbolic
// links, named by their paths without the leading slash. It leaves out the folders that lead to the unit's folders,
// and what is neither a folder, a file nor a link, such as a socket.
func WriteOutside(unit Unit, w io.Writer) error {
	s := newSender(unit, Options{}, nil)
	defer s.close()
	if unit.GitDir != "" {
		if _, err := s.joinWorktrees(); err != nil {
			return err
		}
	}
	archive := tar.NewWriter(w)
	add := func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		link := ""
		switch {
		case entry.Type() == fs.ModeSymlink:
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		case !entry.IsDir() && !entry.Type().IsRegular():
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = strings.TrimPrefix(p, "/")
		if err := archive.WriteHeader(header); err != nil || !entry.Type().IsRegular() {
			return err
		}
		file, err := os.Open(p)
		if err != nil {
			return err
		}
		defer file.Close()
		_, err = io.Copy(archive, file)
		return err
	}
	var visit func(folder string) error
	visit = func(folder string) error {
		entries, err := os.ReadDir(folder)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			p := filepath.Join(folder, entry.Name())
			switch {
			case s.rootOf(p) != "":
			case entry.IsDir() && slices.ContainsFunc(s.roots, func(root string) bool { return within(root, p) }):
				err = visit(p)
			default:
				err = filepath.WalkDir(p, add)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}
	for _, folder := range unit.Within {
		if err := visit(folder); err != nil {
			return err
		}
	}
	return archive.Close()
}

// ReadOutside writes what an archive of WriteOutside holds into folder, at the same paths, durably, and nothing outside
// folder. Each file arrives in the file temporary first, which it replaces, on folder's file system but outside it.
func ReadOutside(r io.Reader, folder, temporary string) error {
	root, err := os.OpenRoot(folder)
	if err != nil {
		return err
	}
	defer root.Close()
	archive := tar.NewReader(r)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return syncFilesystem(folder)
		}
		if err != nil {
			return err
		}
		name := filepath.Clean(header.Name)
		if !filepath.IsLocal(name) {
			return fmt.Errorf("mirror: the archive names %q, outside its folder", header.Name)
		}
		if err := root.MkdirAll(filepath.Dir(name), 0o700); err != nil {
			return err
		}
		mode := header.FileInfo().Mode().Perm()
		switch header.Typeflag {
		case tar.TypeDir:
			if err = root.Mkdir(name, mode|0o700); errors.Is(err, fs.ErrExist) {
				err = nil
			}
		case tar.TypeSymlink:
			if err = root.Remove(name); err == nil || errors.Is(err, fs.ErrNotExist) {
				err = root.Symlink(header.Linkname, name)
			}
		case tar.TypeReg:
			err = place(root, name, temporary, archive, mode, header.ModTime)
		default:
			err = fmt.Errorf("mirror: the archive has %s, which is neither a folder, a file nor a link", header.Name)
		}
		if err != nil {
			return err
		}
	}
}

// place writes contents to the file temporary, in place of what a crash left there, then moves it to name in root, in
// place of a file or link there.
func place(root *os.Root, name, temporary string, contents io.Reader, mode fs.FileMode, modified time.Time) error {
	if err := os.Remove(temporary); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, contents)
	if err == nil {
		err = file.Chmod(mode)
	}
	if closed := file.Close(); err == nil {
		err = closed
	}
	if err == nil {
		err = os.Chtimes(temporary, modified, modified)
	}
	if err != nil {
		return err
	}
	// The folder as root resolves it, so that the move never leaves root.
	parent, err := root.Open(filepath.Dir(name))
	if err != nil {
		return err
	}
	defer parent.Close()
	return unix.Renameat(unix.AT_FDCWD, temporary, int(parent.Fd()), filepath.Base(name))
}
