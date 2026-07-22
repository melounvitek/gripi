package state_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/melounvitek/gripi/internal/state"
)

func TestFileWritesAtomicallyWithOwnerOnlyPermissions(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "custom")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "browser-access.json")
	target := filepath.Join(directory, "symlink-target")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path+".tmp"); err != nil {
		t.Fatal(err)
	}

	file := state.NewFile(path)
	if err := file.Write([]byte(`{"value":"written"}`)); err != nil {
		t.Fatal(err)
	}

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != `{"value":"written"}` {
		t.Fatalf("contents = %q", contents)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
	if contents, err := os.ReadFile(target); err != nil || string(contents) != "untouched" {
		t.Fatalf("predictable temporary path target = %q, %v", contents, err)
	}
	if info, err := os.Stat(directory); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("custom directory mode = %o, %v", info.Mode().Perm(), err)
	}
}

func TestFileSecuresTheDefaultStateDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	directory := filepath.Join(home, ".pi", "gripi")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "state.json")
	if err := state.NewFile(path).Write([]byte("state")); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %o", info.Mode().Perm())
	}
}

func TestFileReadRepairsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	contents, exists, err := state.NewFile(path).Read()
	if err != nil {
		t.Fatal(err)
	}
	if !exists || string(contents) != "existing" {
		t.Fatalf("Read() = %q, %t", contents, exists)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}

func TestFileCreateOnceHasOneConcurrentWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "secret")
	const workers = 40
	start := make(chan struct{})
	results := make(chan string, workers)
	errors := make(chan error, workers)
	var wait sync.WaitGroup
	for index := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			content := fmt.Sprintf("secret-%d", index)
			<-start
			created, err := state.NewFile(path).CreateOnce([]byte(content))
			if err != nil {
				errors <- err
				return
			}
			if created {
				results <- content
			}
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	var winners []string
	for result := range results {
		winners = append(winners, result)
	}
	if len(winners) != 1 {
		t.Fatalf("winners = %v", winners)
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(contents) != winners[0] {
		t.Fatalf("persisted content = %q, winner = %q", contents, winners[0])
	}
}

func TestConcurrentWritesNeverExposePartialState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	file := state.NewFile(path)
	const workers = 20
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			contents := []byte(fmt.Sprintf(`{"writer":%d,"padding":"%s"}`, index, makePadding(index)))
			if err := file.Write(contents); err != nil {
				t.Errorf("Write: %v", err)
			}
		}()
	}
	close(start)
	wait.Wait()

	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted struct {
		Writer  int    `json:"writer"`
		Padding string `json:"padding"`
	}
	if err := json.Unmarshal(contents, &persisted); err != nil {
		t.Fatalf("partial state %q: %v", contents, err)
	}
	if persisted.Padding != makePadding(persisted.Writer) {
		t.Fatalf("mixed write: writer %d, padding length %d", persisted.Writer, len(persisted.Padding))
	}
}

func makePadding(index int) string {
	padding := make([]byte, 1000+index)
	for offset := range padding {
		padding[offset] = byte('a' + index%26)
	}
	return string(padding)
}
