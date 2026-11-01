package worker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/worker/auth"
	"github.com/melounvitek/gripi/internal/worker/mirror"
)

// agentExcluded are the parts of Pi's agent folder that Bootstrap leaves alone on both sides: logins, which the worker
// gets without refresh tokens (see package auth), and sessions, which move to the worker one at a time.
var agentExcluded = []string{"auth.json", "auth.json.lock", "mcp-auth.json", "sessions"}

// Pi is main's Pi, which Bootstrap copies to a worker.
type Pi struct {
	// Command is Gripi's Pi command: pi, or Node and Pi's script, followed by options.
	Command []string
	// AgentDir is main's Pi agent folder, which the worker's Pi uses at the same path.
	AgentDir string
	// ExtensionDir holds Gripi's Pi extensions, which the worker gets at the same path.
	ExtensionDir string
}

// Installation is main's Pi on a worker, as Bootstrap left it.
type Installation struct {
	dir      string // the install folder, absolute
	path     string // the worker's own PATH
	agentDir string
}

// Bootstrap gives the worker main's Pi, before sessions run there. It copies Node and Pi's package into the install
// folder, with a pi that starts them, and Pi's agent folder and Gripi's extensions to their paths here; each copy
// sends only what the worker lacks. Links from the agent folder to the user's own configuration elsewhere are copied
// as what they point to. The localhost ports that Pi's configuration names reach this machine's from the worker for
// as long as the connection lasts. Bootstrap fails unless the worker's Pi then has main's version.
func (conn *Conn) Bootstrap(ctx context.Context, pi Pi) (*Installation, error) {
	node, pkg, script, err := findPi(ctx, pi.Command)
	if err != nil {
		return nil, err
	}
	version, err := piVersion(ctx, pi.Command)
	if err != nil {
		return nil, err
	}
	if auth.OlderPi(version) {
		return nil, fmt.Errorf("workers need Pi %s or later, but this machine has Pi %s", auth.MinimumPiVersion, version)
	}
	ports, err := localPorts(pi.AgentDir)
	if err != nil {
		return nil, err
	}
	for _, port := range ports {
		// localhost listens on both 127.0.0.1 and ::1 on the worker, and reaches whichever one listens here.
		if _, err := conn.Forward(ctx, Forward{Remote: true, Host: "localhost", Listen: port, Connect: port}); err != nil {
			return nil, fmt.Errorf("make port %d on %s reach this machine: %w", port, conn.options.Destination, err)
		}
	}

	// A copy takes folders, so Node goes to bin through a link, which the copy follows.
	nodeLink, err := os.MkdirTemp("", "gripi-node-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(nodeLink)
	if err := os.Symlink(node, filepath.Join(nodeLink, "node")); err != nil {
		return nil, err
	}
	agentDir, err := filepath.EvalSymlinks(pi.AgentDir)
	if err != nil {
		return nil, err
	}
	extensionDir, err := filepath.EvalSymlinks(pi.ExtensionDir)
	if err != nil {
		return nil, err
	}
	for _, folder := range []struct {
		from, to string
		exclude  []string
	}{
		{pkg, conn.installDir + "/lib", nil},
		{nodeLink, conn.installDir + "/bin", []string{"gripi", "pi"}},
		{agentDir, pi.AgentDir, agentExcluded},
		{extensionDir, pi.ExtensionDir, nil},
	} {
		if err := conn.copyFolder(ctx, folder.from, folder.to, folder.exclude); err != nil {
			return nil, fmt.Errorf("copy %s to %s: %w", folder.from, conn.options.Destination, err)
		}
	}

	launcher := "#!/bin/sh\nexec " + ShellQuote(conn.installDir+"/bin/node", conn.installDir+"/lib/"+strings.TrimPrefix(script, pkg+"/")) + " \"$@\"\n"
	output, err := runScript(ctx, conn.Pool, strings.NewReader(launcher), finishScript, conn.installDir)
	if err != nil {
		return nil, fmt.Errorf("start Pi on %s: %w", conn.options.Destination, err)
	}
	path, workerVersion, _ := strings.Cut(strings.TrimSpace(output), "\n")
	if workerVersion != version {
		return nil, fmt.Errorf("Pi on %s has version %q after copying, but %q here", conn.options.Destination, workerVersion, version)
	}
	return &Installation{dir: conn.installDir, path: path, agentDir: pi.AgentDir}, nil
}

// finishScript installs the pi from its input in the install folder, then prints the worker's PATH, as a login shell
// sets it, and the version of the worker's Pi.
const finishScript = `set -e
cd "$1"
cat > bin/.pi.tmp
chmod 700 bin/.pi.tmp
mv -f bin/.pi.tmp bin/pi
path=$("${SHELL:-sh}" -lc 'printenv PATH' < /dev/null 2> /dev/null | tail -n 1)
echo "${path:-$PATH}"
PATH=$PWD/bin:$PATH bin/pi --version
`

// Command returns the command that runs Pi on the worker, given the command that runs it here, which starts with
// Gripi's Pi command: the worker's pi, followed by the same options and arguments.
func (installation *Installation) Command(command []string) []string {
	return append([]string{installation.dir + "/bin/pi"}, command[len(auth.PiCommand(command)):]...)
}

// copyFolder makes the worker's folder to match main's folder from, sending only what differs. What exclude names,
// as in mirror.Options, stays as it is on both sides.
func (conn *Conn) copyFolder(ctx context.Context, from, to string, exclude []string) error {
	return conn.send(ctx, mirror.Unit{Main: from}, to, "", mirror.Options{Exclude: exclude, Dereference: true})
}

// CopyUnit makes the worker's copy of a unit, at the same paths, match main's unit, sending only what differs.
// Ignored folders stay as they are on both sides. With aside, a folder on the worker on the unit's file system, what
// the copy replaces or deletes there goes to aside first.
func (conn *Conn) CopyUnit(ctx context.Context, unit mirror.Unit, aside string) error {
	return conn.send(ctx, unit, unit.Main, aside, mirror.Options{})
}

// UploadMove fills a move's copy, which PrepareMove made, with folder, main's shadow of its home folder, as it is.
func (conn *Conn) UploadMove(ctx context.Context, id, folder string) error {
	return conn.upload(ctx, []string{"--move", id, folder}, mirror.Unit{Main: folder}, mirror.Options{})
}

// UploadSessionFile copies main's file of a session, which needn't exist, to the same path on the worker, where it
// leaves the other files in its folder as they are.
func (conn *Conn) UploadSessionFile(ctx context.Context, file string) error {
	return conn.upload(ctx, []string{"--file", file}, mirror.Unit{Main: filepath.Dir(file), File: file}, mirror.Options{})
}

// send copies main's unit to the folder to on the worker.
func (conn *Conn) send(ctx context.Context, unit mirror.Unit, to, aside string, options mirror.Options) error {
	var arguments []string
	if aside != "" {
		arguments = []string{"--aside", aside}
	}
	return conn.upload(ctx, append(arguments, unit.Main, to), unit, options)
}

// upload copies main's unit to the worker with `gripi worker receive <arguments>`, where the helper makes the copy, so
// that the copy sees the files as the helper's mirrors and sessions do, also in the view of main's home folder.
func (conn *Conn) upload(ctx context.Context, arguments []string, unit mirror.Unit, options mirror.Options) error {
	command, release, err := conn.stream(ctx, "receive", arguments...)
	if err != nil {
		return err
	}
	defer release()
	input, err := command.StdinPipe()
	if err != nil {
		return err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return err
	}
	stderr := &lastLines{}
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return err
	}
	stop := context.AfterFunc(ctx, func() { command.Process.Kill() })
	defer stop()
	stream := struct {
		io.Reader
		io.WriteCloser
	}{output, input}
	err = mirror.Sync(ctx, stream, unit, options)
	if waited := command.Wait(); err == nil {
		err = waited
	}
	if err != nil {
		return fmt.Errorf("%w %s", err, stderr)
	}
	return nil
}

// findPi finds what Gripi's Pi command runs here: Node, and Pi's script in its package folder.
func findPi(ctx context.Context, command []string) (node, pkg, script string, err error) {
	switch words := auth.PiCommand(command); len(words) {
	case 1:
		if script, err = exec.LookPath(words[0]); err != nil {
			return "", "", "", err
		}
		if node, err = interpreter(script); err != nil {
			return "", "", "", err
		}
	case 2:
		if node, err = exec.LookPath(words[0]); err != nil {
			return "", "", "", err
		}
		script = words[1]
	default:
		return "", "", "", fmt.Errorf("the Pi command %q runs neither pi nor Node with Pi's script", command)
	}
	// Node may be a version manager's shim, which runs another program, so Node says where it is.
	output, err := exec.CommandContext(ctx, node, "-p", "process.execPath").Output()
	if err != nil {
		return "", "", "", fmt.Errorf("%s -p process.execPath: %w", node, err)
	}
	node = strings.TrimSpace(string(output))
	if script, err = filepath.EvalSymlinks(script); err != nil {
		return "", "", "", err
	}
	if script, err = filepath.Abs(script); err != nil {
		return "", "", "", err
	}
	for pkg = filepath.Dir(script); ; pkg = filepath.Dir(pkg) {
		if _, err := os.Stat(filepath.Join(pkg, "package.json")); err == nil {
			return node, pkg, script, nil
		}
		if pkg == filepath.Dir(pkg) {
			return "", "", "", fmt.Errorf("no package.json is above %s, so Pi's package is unknown", script)
		}
	}
}

// interpreter returns the program that a script's #! line runs, directly or through env, such as Pi's Node.
func interpreter(script string) (string, error) {
	file, err := os.Open(script)
	if err != nil {
		return "", err
	}
	defer file.Close()
	line, _ := bufio.NewReader(file).ReadString('\n')
	words, found := strings.CutPrefix(line, "#!")
	fields := strings.Fields(words)
	if !found || len(fields) == 0 {
		return "", fmt.Errorf("%s does not start with #!, so its Node is unknown", script)
	}
	if filepath.Base(fields[0]) != "env" {
		return fields[0], nil
	}
	for _, field := range fields[1:] {
		if !strings.HasPrefix(field, "-") && !strings.Contains(field, "=") {
			return exec.LookPath(field)
		}
	}
	return "", fmt.Errorf("the #! line of %s names no program", script)
}

func piVersion(ctx context.Context, command []string) (string, error) {
	words := auth.PiCommand(command)
	process := exec.CommandContext(ctx, words[0], append(slices.Clone(words[1:]), "--version")...)
	process.Env = agent.ScrubbedEnvironment(os.Environ())
	output, err := process.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			err = fmt.Errorf("%w: %s", err, bytes.TrimSpace(exit.Stderr))
		}
		return "", fmt.Errorf("pi --version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

// localPorts returns the ports of the localhost URLs in Pi's configuration here: providers' base URLs in models.json
// and MCP servers' URLs in mcp.json.
func localPorts(agentDir string) ([]int, error) {
	var ports []int
	for _, file := range []struct{ name, key string }{{"models.json", "baseUrl"}, {"mcp.json", "url"}} {
		data, err := os.ReadFile(filepath.Join(agentDir, file.name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var configuration any
		if err := json.Unmarshal(auth.PlainJSON(data), &configuration); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(agentDir, file.name), err)
		}
		for _, address := range valuesOf(configuration, file.key) {
			parsed, err := url.Parse(address)
			if err != nil {
				continue
			}
			host := parsed.Hostname()
			port, _ := strconv.Atoi(parsed.Port())
			if (host == "localhost" || host == "127.0.0.1" || host == "::1") && port > 0 && !slices.Contains(ports, port) {
				ports = append(ports, port)
			}
		}
	}
	return ports, nil
}

// valuesOf returns the strings under key anywhere in a parsed JSON value.
func valuesOf(value any, key string) []string {
	var found []string
	switch value := value.(type) {
	case map[string]any:
		for name, child := range value {
			if text, ok := child.(string); ok && name == key {
				found = append(found, text)
			} else {
				found = append(found, valuesOf(child, key)...)
			}
		}
	case []any:
		for _, child := range value {
			found = append(found, valuesOf(child, key)...)
		}
	}
	return found
}
