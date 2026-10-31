package worker

import (
	"context"
	"fmt"
	"net"
	"strconv"
)

// Forwarded is a port of a worker that main makes available at a port of its own.
type Forwarded struct {
	Port, Local int
}

// forwarded is a forward that a worker's connection runs, until cancel.
type forwarded struct {
	local  int
	cancel func() error
}

// freePorts is how many ports Forward tries, from the worker's port up, for one that is free on main.
const freePorts = 100

// Forward makes a port on the loopback of the worker that runs the repository of folder available on main's loopback,
// at the same port or, when that is taken, at the next free one, which it returns. The forward lasts until Unforward
// or until main's connection to the worker ends.
func (manager *Manager) Forward(ctx context.Context, folder string, port int) (local int, err error) {
	placement, placed := manager.options.Placements.Find(folder)
	if !placed {
		return 0, refuse("%s is not on a worker", folder)
	}
	manager.mu.Lock()
	worker := manager.workers[placement.Worker]
	var conn *Conn
	if worker != nil {
		conn = worker.conn
		if forwarded, found := worker.forwards[port]; found {
			manager.mu.Unlock()
			return forwarded.local, nil
		}
	}
	manager.mu.Unlock()
	if conn == nil {
		return 0, refuse("worker %s is not connected; its ports can be forwarded while its sessions run", placement.Worker)
	}
	for local = port; local < min(port+freePorts, 65536); local++ {
		listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(local)))
		if err != nil {
			continue
		}
		listener.Close()
		// localhost forwards both 127.0.0.1 and ::1, where dev servers such as Vite's often listen.
		cancel, err := conn.Forward(ctx, Forward{Host: "localhost", Listen: local, Connect: port})
		if err != nil {
			return 0, err
		}
		return manager.addForward(worker, conn, port, forwarded{local: local, cancel: cancel})
	}
	return 0, fmt.Errorf("no port from %d to %d is free on this machine", port, local-1)
}

// addForward records a forward that a worker's connection runs, unless the connection has ended meanwhile, or another
// Forward of the port came first.
func (manager *Manager) addForward(worker *remote, conn *Conn, port int, added forwarded) (int, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if worker.conn != conn {
		return 0, fmt.Errorf("worker %s disconnected", worker.name)
	}
	if existing, found := worker.forwards[port]; found {
		added.cancel()
		return existing.local, nil
	}
	if worker.forwards == nil {
		worker.forwards = map[int]forwarded{}
	}
	worker.forwards[port] = added
	return added.local, nil
}

// Unforward stops forwarding a port of the worker that runs the repository of folder.
func (manager *Manager) Unforward(folder string, port int) error {
	placement, placed := manager.options.Placements.Find(folder)
	if !placed {
		return refuse("%s is not on a worker", folder)
	}
	manager.mu.Lock()
	var current forwarded
	found := false
	if worker := manager.workers[placement.Worker]; worker != nil {
		current, found = worker.forwards[port]
		delete(worker.forwards, port)
	}
	manager.mu.Unlock()
	if !found {
		return refuse("port %d of worker %s is not forwarded", port, placement.Worker)
	}
	return current.cancel()
}
