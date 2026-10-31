package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/melounvitek/gripi/internal/agent/pi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
	remote "github.com/melounvitek/gripi/internal/worker"
)

// newWorkers runs the sessions of the repositories placed on workers there, with Pi's adapter's launcher. It connects
// to workers with the options of connection.
func newWorkers(cfg config.Config, piAdapter *pi.Adapter, handler http.Handler, connection remote.Options) (*remote.Manager, error) {
	placements, err := remote.LoadPlacements(cfg.WorkerPlacementsPath)
	if err != nil {
		return nil, err
	}
	tree, workerExtension, err := piAdapter.ExtensionFiles()
	if err != nil {
		return nil, err
	}
	return remote.NewManager(remote.ManagerOptions{
		Destination: func(name string) (string, error) {
			workers, err := readWorkers(cfg.WorkersPath)
			if err != nil {
				return "", err
			}
			for _, worker := range workers {
				if worker.Name == name {
					return worker.Destination, nil
				}
			}
			return "", fmt.Errorf("no worker is named %q; 'gripi worker list' shows the workers", name)
		},
		Placements: placements, StateDir: stateDir(cfg),
		Pi:              remote.Pi{Command: cfg.PiCommand, AgentDir: cfg.PiAgentDir, ExtensionDir: filepath.Dir(tree)},
		WorkerExtension: workerExtension, SessionsRoot: cfg.SessionsRoot, SocketPath: cfg.SocketPath, Store: piAdapter.Store(),
		ServeSocket: func(name string, placement remote.WorkerPlacement) (string, func() error, error) {
			return startWorkerSocket(cfg, handler, name, placement)
		},
		Connection: connection,
	}), nil
}

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
