package main

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/agent/pi"
	"github.com/melounvitek/gripi/internal/config"
	gateway "github.com/melounvitek/gripi/internal/server"
	remote "github.com/melounvitek/gripi/internal/worker"
)

// newWorkers runs the sessions placed on workers there, with Pi's adapter's launcher. It connects to workers with the
// options of connection, in the view of the gateway user's home folder.
func newWorkers(cfg config.Config, piAdapter *pi.Adapter, handler http.Handler, connection remote.Options) (*remote.Manager, error) {
	connection.Home = cfg.Home
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
		GatewayPaths: []string{
			filepath.Dir(cfg.EnvPath), cfg.SessionCwdsPath, cfg.AttachmentsRoot, cfg.WebPushVAPIDPath, cfg.PushSubscriptionsPath, cfg.ReadStatePath,
			cfg.PinnedSessionsPath, cfg.SessionTagsPath, cfg.EnvironmentPath, cfg.BrowserAccessPath, cfg.WorkspaceSecretPath, cfg.WorkspaceAccessPath,
			cfg.WorkspaceOwnershipPath, cfg.WorkersPath, cfg.WorkerPlacementsPath, cfg.RestartPath, cfg.SocketPath,
		},
		Pi:              remote.Pi{Command: cfg.PiCommand, AgentDir: cfg.PiAgentDir, ExtensionDir: filepath.Dir(tree)},
		WorkerExtension: workerExtension, SessionsRoot: cfg.SessionsRoot, SocketPath: cfg.SocketPath, Store: piAdapter.Store(),
		Rewritten: handler.(interface{ Guard() agent.Guard }).Guard().Rewritten,
		ServeSocket: func(name string, placement remote.WorkerPlacement) (string, func() error, error) {
			return startWorkerSocket(cfg, handler, name, placement)
		},
		Connection: connection,
	}), nil
}

// startWorkerSocket serves the gripi commands of a connected worker on a socket of its own, in a private folder under
// the gateway's state folder, where they reach only the sessions that placement says are on the worker. Forward the socket to the
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

// gatewayWorkers are the workers as the gateway sees them: the manager's, with the workers that `gripi worker add`
// saved in path.
type gatewayWorkers struct {
	*remote.Manager
	path string
}

func (workers gatewayWorkers) List() ([]gateway.Worker, error) {
	saved, err := readWorkers(workers.path)
	list := make([]gateway.Worker, 0, len(saved))
	for _, worker := range saved {
		status := workers.Status(worker.Name)
		list = append(list, gateway.Worker{Name: worker.Name, Destination: worker.Destination, State: string(status.State), GoneAt: status.GoneAt})
	}
	return list, err
}

func (workers gatewayWorkers) Location(key string) (gateway.WorkerLocation, bool) {
	location, found := workers.Manager.Location(key)
	ports := make([]gateway.WorkerPort, 0, len(location.Forwards))
	for _, forwarded := range location.Forwards {
		ports = append(ports, gateway.WorkerPort(forwarded))
	}
	return gateway.WorkerLocation{
		Worker: location.Worker, State: string(location.State), GoneAt: location.GoneAt, Repository: location.Repository,
		Copying: location.Copying, Ports: ports,
	}, found
}

func (workers gatewayWorkers) Notices(key string) []gateway.WorkerNotice {
	var notices []gateway.WorkerNotice
	for _, notice := range workers.Manager.Notices(key) {
		shown := gateway.WorkerNotice{
			Repository: notice.Repository, Worker: notice.Worker, Aside: filepath.Join(notice.Recovery, "worker"),
			Paths: []string{}, Items: []gateway.WorkerNoticeItem{}, Refs: append([]string{}, notice.Result.Refs...), Text: notice.Result.Notice, Time: notice.Time,
		}
		for _, item := range notice.Result.Aside {
			shown.Paths = append(shown.Paths, item.Path)
			shown.Items = append(shown.Items, gateway.WorkerNoticeItem(item))
		}
		notices = append(notices, shown)
	}
	return notices
}
