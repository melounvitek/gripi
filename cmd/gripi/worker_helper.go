package main

import (
	"context"
	"flag"
	"io"
	"os/signal"
	"syscall"

	"github.com/melounvitek/gripi/internal/worker/helper"
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
	options := helper.Options{RuntimeDir: *runtimeDir, Lease: *lease}
	var err error
	if options.RuntimeDir == "" {
		if options.RuntimeDir, err = helper.DefaultRuntimeDir(); err != nil {
			return failure(stderr, "worker", err)
		}
	}
	if options.Cgroup, err = helper.DelegatedCgroup(); err != nil {
		return failure(stderr, "worker", err)
	}
	if options.Version, err = helper.FileVersion("/proc/self/exe"); err != nil {
		return failure(stderr, "worker", err)
	}
	// After main has gone, a failed write must not kill the helper before it has stopped the sessions.
	signal.Ignore(syscall.SIGPIPE)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer stop()
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
	if *runtimeDir == "" {
		if *runtimeDir, err = helper.DefaultRuntimeDir(); err != nil {
			return failure(stderr, "worker", err)
		}
	}
	if err := helper.Attach(*runtimeDir, positional[0], positional[1], stdin, stdout); err != nil {
		return failure(stderr, "worker", err)
	}
	return 0
}
