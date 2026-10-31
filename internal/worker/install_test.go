package worker

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/melounvitek/gripi/internal/worker/helper"
)

var builds struct {
	once                   sync.Once
	folder, gripi, failing string
	err                    error
}

func TestMain(m *testing.M) {
	if pidFile := os.Getenv("GRIPI_TEST_RELAY"); pidFile != "" {
		relay(pidFile, os.Args[1], os.Args[2])
		return
	}
	code := m.Run()
	if builds.folder != "" {
		os.RemoveAll(builds.folder)
	}
	os.Exit(code)
}

// buildExecutables builds Gripi as main uploads it to workers, and a static executable that fails when it runs.
func buildExecutables(t *testing.T) (gripi, failing string) {
	t.Helper()
	builds.once.Do(func() {
		if builds.folder, builds.err = os.MkdirTemp("", "gripi-worker-test-"); builds.err != nil {
			return
		}
		builds.gripi, builds.failing = filepath.Join(builds.folder, "gripi"), filepath.Join(builds.folder, "failing")
		source := filepath.Join(builds.folder, "failing.go")
		if builds.err = os.WriteFile(source, []byte("package main\n\nimport \"os\"\n\nfunc main() { os.Exit(3) }\n"), 0600); builds.err != nil {
			return
		}
		for _, arguments := range [][]string{{"-o", builds.gripi, "github.com/melounvitek/gripi/cmd/gripi"}, {"-o", builds.failing, source}} {
			command := exec.Command("go", append([]string{"build"}, arguments...)...)
			command.Env = append(os.Environ(), "CGO_ENABLED=0")
			if output, err := command.CombinedOutput(); err != nil {
				builds.err = fmt.Errorf("go build: %v\n%s", err, output)
				return
			}
		}
	})
	if builds.err != nil {
		t.Fatal(builds.err)
	}
	return builds.gripi, builds.failing
}

func TestHelperVersionRefusesExecutablesThatAreNotStatic(t *testing.T) {
	gripi, _ := buildExecutables(t)
	if version, err := HelperVersion(gripi); err != nil || version != must(helper.FileVersion(gripi)) {
		t.Fatalf("HelperVersion of a static build = %q, %v", version, err)
	}
	dynamic, err := exec.LookPath("ls")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := HelperVersion(dynamic); err == nil || !strings.Contains(err.Error(), "CGO_ENABLED=0") {
		t.Fatalf("HelperVersion of %s = %v", dynamic, err)
	}
	script := filepath.Join(t.TempDir(), "gripi")
	os.WriteFile(script, []byte("#!/bin/sh\n"), 0700)
	if _, err := HelperVersion(script); err == nil {
		t.Fatal("HelperVersion accepts a script")
	}
}

func TestInstallHelperUploadsOnlyAnotherVersion(t *testing.T) {
	home := fakeSSH(t)
	gripi, _ := buildExecutables(t)
	version := must(HelperVersion(gripi))
	pool := newTestPool(t)
	ctx := context.Background()
	installed := filepath.Join(home, "gripi", "worker", "bin", "gripi")

	dir, err := installHelper(ctx, pool, gripi, version, "gripi/worker")
	if err != nil || dir != filepath.Join(home, "gripi", "worker") || must(helper.FileVersion(installed)) != version {
		t.Fatalf("installHelper = %q, %v", dir, err)
	}
	for _, path := range []string{filepath.Join(home, "gripi"), filepath.Dir(installed), installed} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("%s = %v, %v", path, info, err)
		}
	}
	if _, err := installHelper(ctx, pool, gripi, version, "gripi/worker"); err != nil || len(sshCalls(t, "mktemp")) != 1 {
		t.Fatalf("installHelper of the installed version = %v, uploads %d", err, len(sshCalls(t, "mktemp")))
	}

	os.WriteFile(installed, []byte("an older helper"), 0700)
	if _, err := installHelper(ctx, pool, gripi, version, "gripi/worker"); err != nil || must(helper.FileVersion(installed)) != version {
		t.Fatalf("installHelper over another version = %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Dir(installed)); len(entries) != 1 {
		t.Fatalf("bin holds %v", entries)
	}
	absolute := filepath.Join(t.TempDir(), "worker")
	if dir, err := installHelper(ctx, pool, gripi, version, absolute); err != nil || dir != absolute {
		t.Fatalf("installHelper into %s = %q, %v", absolute, dir, err)
	}
}

func TestInstallHelperKeepsTheHelperWhenTheNewOneDoesNotRun(t *testing.T) {
	home := fakeSSH(t)
	gripi, failing := buildExecutables(t)
	pool := newTestPool(t)
	ctx := context.Background()
	installed := filepath.Join(home, "worker", "bin", "gripi")
	if _, err := installHelper(ctx, pool, gripi, must(HelperVersion(gripi)), "worker"); err != nil {
		t.Fatal(err)
	}

	_, err := installHelper(ctx, pool, failing, must(HelperVersion(failing)), "worker")
	if err == nil || !strings.Contains(err.Error(), "does not run") {
		t.Fatalf("installHelper of a failing helper = %v", err)
	}
	if must(helper.FileVersion(installed)) != must(helper.FileVersion(gripi)) {
		t.Fatal("the failing helper replaced the working one")
	}
	if entries, _ := os.ReadDir(filepath.Dir(installed)); len(entries) != 1 {
		t.Fatalf("bin holds %v", entries)
	}
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
