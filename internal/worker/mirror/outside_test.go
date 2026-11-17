//go:build linux

package mirror

import (
	"archive/tar"
	"bytes"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// What a moved session's copy holds in the folders that show it, outside the unit's folders, which a mirror never
// sends, goes to main in an archive: what the session made there, but neither the folders that only lead to the
// unit's folders nor the files of the unit, also of a worktree that joined it, nor sockets.
func TestWhatACopyHoldsOutsideItsFoldersTravelsInAnArchive(t *testing.T) {
	repository := newRepo(t)
	featA := repository.Worktrees[0]
	asACopy(t, repository)
	base := filepath.Dir(filepath.Dir(repository.Main))
	unit := Unit{Main: featA, GitDir: repository.GitDir, Within: []string{base}}
	featC := filepath.Join(filepath.Dir(featA), "feat-c")
	git(t, featA, "worktree", "add", "-q", "-b", "feat-c", featC)
	writeFile(t, featC, "new.txt", "in feat-c\n")
	writeFile(t, base, "NOTES.md", "notes\n")
	writeFile(t, base, "api/main.go", "api\n")
	writeFile(t, repository.Main, "todo.txt", "in the main checkout, which isn't the unit's\n")
	if err := os.Symlink("src/proj", filepath.Join(base, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(base, "api", "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	socket, err := net.Listen("unix", filepath.Join(base, "api", "socket"))
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()

	var archive bytes.Buffer
	if err := WriteOutside(unit, &archive); err != nil {
		t.Fatal(err)
	}
	main := t.TempDir()
	// Again, as a return that runs again does.
	for range 2 {
		if err := ReadOutside(bytes.NewReader(archive.Bytes()), main); err != nil {
			t.Fatal(err)
		}
	}

	var found []string
	filepath.WalkDir(main+base, func(p string, _ fs.DirEntry, err error) error {
		if err == nil && p != main+base {
			found = append(found, strings.TrimPrefix(p, main+base+"/"))
		}
		return err
	})
	if want := []string{"NOTES.md", "api", "api/empty", "api/main.go", "link", "src", "src/proj", "src/proj/todo.txt"}; !slices.Equal(found, want) {
		t.Errorf("main got\n%q\nwant\n%q", found, want)
	}
	for path, want := range map[string]string{"NOTES.md": "notes\n", "api/main.go": "api\n"} {
		if got, _ := os.ReadFile(filepath.Join(main+base, path)); string(got) != want {
			t.Errorf("main's %s = %q, want %q", path, got, want)
		}
	}
	if target, err := os.Readlink(main + filepath.Join(base, "link")); err != nil || target != "src/proj" {
		t.Errorf("main's link = %q, %v", target, err)
	}

	// An archive never writes outside main's folder.
	var hostile bytes.Buffer
	writer := tar.NewWriter(&hostile)
	writer.WriteHeader(&tar.Header{Name: "../escaped", Typeflag: tar.TypeReg, Mode: 0o600})
	writer.Close()
	into := filepath.Join(t.TempDir(), "into")
	os.Mkdir(into, 0o700)
	if err := ReadOutside(&hostile, into); err == nil {
		t.Error("ReadOutside took a path outside its folder")
	}
	if _, err := os.Lstat(filepath.Join(filepath.Dir(into), "escaped")); err == nil {
		t.Error("ReadOutside wrote outside its folder")
	}
}
