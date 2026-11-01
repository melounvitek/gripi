package main

import (
	"context"
	"flag"
	"io"
	"log"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/melounvitek/gripi/internal/worker/auth"
	"github.com/melounvitek/gripi/internal/worker/helper"
	"github.com/melounvitek/gripi/internal/worker/view"
)

// serveWorker runs the helper on a worker, under the systemd unit that main starts it in.
func serveWorker(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker serve", flag.ContinueOnError)
	lease := flags.Duration("lease", helper.DefaultLease, "")
	runtimeDir := flags.String("runtime-dir", "", "")
	if positional, err := parseArguments(flags, arguments); err != nil {
		return usageError(stderr, "worker", err.Error())
	} else if len(positional) > 0 {
		return usageError(stderr, "worker", "serve takes no arguments besides its flags")
	}
	options := helper.Options{Lease: *lease}
	var err error
	if options.RuntimeDir, err = runtimeDirectory(*runtimeDir); err != nil {
		return failure(stderr, "worker", err)
	}
	if options.Cgroup, err = helper.DelegatedCgroup(); err != nil {
		return failure(stderr, "worker", err)
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

// mirrorWorker connects main's copy of a unit to the unit's mirror in the helper.
func mirrorWorker(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker mirror", flag.ContinueOnError)
	runtimeDir := flags.String("runtime-dir", "", "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "worker", err.Error())
	}
	if len(positional) != 2 {
		return usageError(stderr, "worker", "mirror takes a unit's main folder and a helper instance")
	}
	folder, err := runtimeDirectory(*runtimeDir)
	if err != nil {
		return failure(stderr, "worker", err)
	}
	if err := helper.AttachMirror(folder, positional[0], positional[1], stdin, stdout); err != nil {
		return failure(stderr, "worker", err)
	}
	return 0
}

// receiveWorker makes a folder on the worker a copy of a folder on main, which main sends over the channel.
func receiveWorker(arguments []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("worker receive", flag.ContinueOnError)
	aside := flags.String("aside", "", "")
	positional, err := parseArguments(flags, arguments)
	if err != nil {
		return usageError(stderr, "worker", err.Error())
	}
	if len(positional) != 2 {
		return usageError(stderr, "worker", "receive takes main's folder and the folder here")
	}
	if err := helper.Receive(context.Background(), stdin, stdout, positional[0], positional[1], *aside); err != nil {
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
