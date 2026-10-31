package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
)

// startWorkerSocket serves the gripi commands of a connected worker on a socket of its own, in a private folder under
// the gateway's state folder, where they reach only what placement says is on the worker. Forward the socket to the
// worker at cfg.SocketPath, where gripi looks for it: worker.Forward{Remote: true, ListenSocket: cfg.SocketPath,
// ConnectSocket: socket}. stop closes the socket.
func startWorkerSocket(cfg config.Config, handler http.Handler, name string, placement gateway.WorkerPlacement) (socket string, stop func() error, err error) {
	if name == "" || strings.ContainsRune(name, filepath.Separator) {
		return "", nil, fmt.Errorf("the worker name %q cannot name a socket", name)
	}
	socket = filepath.Join(stateDir(cfg), "workers", name+".sock")
	server, err := startLocalServer(handler.(interface {
		Worker(gateway.WorkerPlacement) http.Handler
	}).Worker(placement), socket)
	if err != nil {
		return "", nil, err
	}
	return socket, server.Close, nil
}
