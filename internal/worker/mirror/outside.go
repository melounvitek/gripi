package mirror

import (
	"archive/tar"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
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

// ReadOutside writes what an archive of WriteOutside holds into folder, at the same paths, durably, each file through a
// temporary file next to it, and nothing outside folder.
func ReadOutside(r io.Reader, folder string) error {
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
			err = writeIn(root, name, archive, mode, header.ModTime)
		default:
			err = fmt.Errorf("mirror: the archive has %s, which is neither a folder, a file nor a link", header.Name)
		}
		if err != nil {
			return err
		}
	}
}

// writeIn writes the file name in root with contents, through a new temporary file next to it.
func writeIn(root *os.Root, name string, contents io.Reader, mode fs.FileMode, modified time.Time) error {
	temporary := filepath.Join(filepath.Dir(name), ".gripi-"+rand.Text())
	file, err := root.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
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
		err = root.Chtimes(temporary, modified, modified)
	}
	if err == nil {
		err = root.Rename(temporary, name)
	}
	if err != nil {
		root.Remove(temporary)
	}
	return err
}
