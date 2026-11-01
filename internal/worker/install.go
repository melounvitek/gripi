package worker

import (
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/melounvitek/gripi/internal/worker/helper"
)

// HelperVersion returns the version of a Gripi executable that main can upload as the helper: the SHA-256 of its
// contents. Workers have main's system and architecture, but not necessarily its C library, so the executable must
// be static.
func HelperVersion(executable string) (string, error) {
	file, err := elf.Open(executable)
	if err != nil {
		return "", fmt.Errorf("%s cannot run on a Linux worker: %w", executable, err)
	}
	defer file.Close()
	for _, program := range file.Progs {
		if program.Type == elf.PT_INTERP {
			return "", fmt.Errorf("%s is linked dynamically, so it may not run on a worker; build Gripi with CGO_ENABLED=0", executable)
		}
	}
	return helper.FileVersion(executable)
}

// installedScript prints the worker user's home folder and the install folder as an absolute path, then the SHA-256
// of the helper in it, if any.
const installedScript = `umask 077
echo "$HOME"
case $1 in /*) dir=$1 ;; *) dir=$HOME/$1 ;; esac
mkdir -p "$dir/bin" && cd "$dir" && pwd -P || exit
sha256sum 2>/dev/null < bin/gripi || true
`

// uploadScript installs the helper from its input into the install folder, if it arrived whole and runs.
const uploadScript = `set -e
umask 077
cd "$1"
temporary=$(mktemp bin/.gripi.XXXXXX)
trap 'rm -f "$temporary"' EXIT
cat > "$temporary"
[ "$(sha256sum < "$temporary")" = "$2  -" ] || { echo "the helper did not arrive whole" >&2; exit 1; }
chmod 700 "$temporary"
"./$temporary" worker --help > /dev/null || { echo "the uploaded helper does not run" >&2; exit 1; }
mv -f "$temporary" bin/gripi
`

// installHelper makes sure that the worker's helper is the executable of version, and returns the install folder as an
// absolute path on the worker, and the worker user's home folder. dir is relative to that home unless it is absolute.
func installHelper(ctx context.Context, pool commander, executable, version, dir string) (installed, home string, err error) {
	output, err := runScript(ctx, pool, nil, installedScript, dir)
	if err != nil {
		return "", "", fmt.Errorf("check the helper on the worker: %w", err)
	}
	lines := strings.SplitN(output, "\n", 3)
	if len(lines) < 3 {
		return "", "", fmt.Errorf("unexpected answer from the worker: %q", output)
	}
	home, installed = lines[0], lines[1]
	if hash, _, _ := strings.Cut(lines[2], " "); hash == version {
		return installed, home, nil
	}
	file, err := os.Open(executable)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	if _, err := runScript(ctx, pool, file, uploadScript, installed, version); err != nil {
		return "", "", fmt.Errorf("upload the helper: %w", err)
	}
	return installed, home, nil
}

// commander starts commands for the worker user's shell, each in an ssh channel of its own, as a Pool does.
type commander interface {
	Command(ctx context.Context, remote string) (command *exec.Cmd, release func(), err error)
}

// runScript runs a sh script on the worker, whatever the user's shell is, and returns its output.
func runScript(ctx context.Context, pool commander, input io.Reader, script string, arguments ...string) (string, error) {
	return runWords(ctx, pool, input, append([]string{"sh", "-c", script, "sh"}, arguments...)...)
}

// runWords runs a command on the worker and returns its output.
func runWords(ctx context.Context, pool commander, input io.Reader, words ...string) (string, error) {
	command, release, err := pool.Command(ctx, ShellQuote(words...))
	if err != nil {
		return "", err
	}
	defer release()
	var output, errors bytes.Buffer
	command.Stdin, command.Stdout, command.Stderr = input, &output, &errors
	if err := command.Start(); err != nil {
		return "", err
	}
	stop := context.AfterFunc(ctx, func() { command.Process.Kill() })
	defer stop()
	if err := command.Wait(); err != nil {
		return "", fmt.Errorf("%v: %s", err, strings.TrimSpace(errors.String()))
	}
	return output.String(), nil
}
