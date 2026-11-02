package main

import (
	"cmp"
	"context"
	"flag"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/melounvitek/gripi/internal/worker/auth"
	"github.com/melounvitek/gripi/internal/worker/dockerproxy"
	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/view"
)

// serveWorker runs the helper on a worker, under the systemd unit that main starts it in, in the view of main's home
// folder.
func serveWorker(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker serve", flag.ContinueOnError)
	lease := flags.Duration("lease", helper.DefaultLease, "")
	runtimeDir := flags.String("runtime-dir", "", "")
	home := flags.String("home", "", "")
	// Gripi's folder on the worker, as Docker sees it, for the moves' copies.
	dataDir := flags.String("data-dir", "", "")
	// The cgroup that systemd delegated to the unit, and the worker user's own home folder, which serve passes on to
	// itself in the view.
	cgroup := flags.String("cgroup", "", "")
	ownHome := flags.String("own-home", "", "")
	if positional, err := parseArguments(flags, arguments); err != nil {
		return usageError(stderr, "worker", err.Error())
	} else if len(positional) > 0 {
		return usageError(stderr, "worker", "serve takes no arguments besides its flags")
	}
	options := helper.Options{Lease: *lease, Cgroup: *cgroup, Home: *home, OwnHome: cmp.Or(*ownHome, *home), DataDir: *dataDir}
	var err error
	if options.RuntimeDir, err = runtimeDirectory(*runtimeDir); err != nil {
		return failure(stderr, "worker", err)
	}
	if options.Cgroup == "" {
		if options.Cgroup, err = helper.DelegatedCgroup(); err != nil {
			return failure(stderr, "worker", err)
		}
	}
	if *ownHome == "" && view.Needed(*home) {
		own, err := os.UserHomeDir()
		if err != nil {
			return failure(stderr, "worker", err)
		}
		// Only the unit's first process can take the delegated cgroup, as its only one. The processes of the view and
		// the sessions stay in the unit, where systemd's watchdog and KillMode=control-group reach them.
		code, err := view.Run(*home, append([]string{"/proc/self/exe", "worker", "serve", "--cgroup", options.Cgroup, "--own-home", own}, arguments...), stdin, stdout, stderr)
		if err != nil {
			return failure(stderr, "worker", err)
		}
		return code
	}
	if options.Version, err = helper.FileVersion("/proc/self/exe"); err != nil {
		return failure(stderr, "worker", err)
	}
	// Main logs what the helper writes, with times of its own.
	log.SetFlags(0)
	// After main has gone, a failed write must not kill the helper before it has stopped the sessions.
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
	options.ApplyAuth = func(path, contents string) error { return auth.WriteLocked(ctx, path, []byte(contents)) }
	options.DockerUpstream = dockerproxy.Upstream(os.Environ())
	if *ownHome != "" {
		// The Docker daemon runs outside the view, so the sessions get a socket that moves the host paths in their
		// requests from main's home folder to the worker user's own, and passes them on to the helper's Docker.
		options.Docker = func(ctx context.Context, socket string) error {
			return dockerproxy.Serve(ctx, dockerproxy.Options{Listen: socket, Upstream: options.DockerUpstream, Mappings: []dockerproxy.Mapping{{From: *home, To: *ownHome}}})
		}
	}
	if err := helper.Serve(ctx, stdin, stdout, options); err != nil {
		return failure(stderr, "worker", err)
	}
	return 0
}

// attachWorker passes the RPC of a session's Pi through over the channel that main opened for it.
func attachWorker(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker attach", flag.ContinueOnError)
	runtimeDir := flags.String("runtime-dir", "", "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "worker", err.Error())
	}
	if len(positional) != 2 {
		return usageError(stderr, "worker", "attach takes a session and a helper instance")
	}
	folder, err := runtimeDirectory(*runtimeDir)
	if err != nil {
		return failure(stderr, "worker", err)
	}
	if err := helper.Attach(folder, positional[0], positional[1], stdin, stdout); err != nil {
		return failure(stderr, "worker", err)
	}
	return 0
}

// mirrorWorker connects main's copy of a unit to the unit's mirror in the helper, or with --move, to a mirror of a
// move: of the move's copy, or with --file, of the session's file.
func mirrorWorker(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker mirror", flag.ContinueOnError)
	runtimeDir := flags.String("runtime-dir", "", "")
	move := flags.String("move", "", "")
	file := flags.String("file", "", "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "worker", err.Error())
	}
	if *move != "" && len(positional) != 1 {
		return usageError(stderr, "worker", "mirror --move takes a helper instance")
	}
	if *move == "" && len(positional) != 2 {
		return usageError(stderr, "worker", "mirror takes a unit's main folder and a helper instance")
	}
	folder, err := runtimeDirectory(*runtimeDir)
	if err != nil {
		return failure(stderr, "worker", err)
	}
	if *move != "" {
		err = helper.AttachMoveMirror(folder, *move, *file, positional[0], stdin, stdout)
	} else {
		err = helper.AttachMirror(folder, positional[0], positional[1], stdin, stdout)
	}
	if err != nil {
		return failure(stderr, "worker", err)
	}
	return 0
}

// hostMove runs a move's mirrors and Docker socket for the helper, which starts it in the move's view.
func hostMove(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(arguments) > 0 {
		return usageError(stderr, "worker", "move-host takes no arguments")
	}
	log.SetFlags(0)
	if err := helper.Host(stdin, stdout); err != nil {
		return failure(stderr, "worker", err)
	}
	return 0
}

// receiveWorker connects the channel that main opened for a copy of its folder to the helper, which makes the copy:
// with --move, the move's copy, and with --file, a copy of main's file at the same path.
func receiveWorker(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker receive", flag.ContinueOnError)
	runtimeDir := flags.String("runtime-dir", "", "")
	aside := flags.String("aside", "", "")
	move := flags.String("move", "", "")
	file := flags.String("file", "", "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "worker", err.Error())
	}
	switch {
	case *move != "" && len(positional) != 2:
		return usageError(stderr, "worker", "receive --move takes main's folder and a helper instance")
	case *file != "" && len(positional) != 1:
		return usageError(stderr, "worker", "receive --file takes a helper instance")
	case *move == "" && *file == "" && len(positional) != 3:
		return usageError(stderr, "worker", "receive takes main's folder, the folder here and a helper instance")
	}
	folder, err := runtimeDirectory(*runtimeDir)
	if err != nil {
		return failure(stderr, "worker", err)
	}
	switch {
	case *move != "":
		err = helper.AttachReceiveMove(folder, *move, positional[0], positional[1], stdin, stdout)
	case *file != "":
		err = helper.AttachReceiveFile(folder, *file, positional[0], stdin, stdout)
	default:
		err = helper.AttachReceive(folder, positional[0], positional[1], *aside, positional[2], stdin, stdout)
	}
	if err != nil {
		return failure(stderr, "worker", err)
	}
	return 0
}

// enterWorker runs a command in the private view of main's home folder (package view), or as it is when this user's
// home folder is main's.
func enterWorker(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker enter", flag.ContinueOnError)
	home := flags.String("home", "", "")
	command, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "worker", err.Error())
	}
	if len(command) == 0 {
		return usageError(stderr, "worker", "enter takes a command after --")
	}
	code, err := view.Run(*home, command, stdin, stdout, stderr)
	if err != nil {
		return failure(stderr, "worker", err)
	}
	return code
}

// runtimeDirectory is the helper's socket folder as an absolute path, because Pi gets the path of a socket in it.
func runtimeDirectory(flag string) (string, error) {
	if flag == "" {
		return helper.DefaultRuntimeDir()
	}
	return filepath.Abs(flag)
}
