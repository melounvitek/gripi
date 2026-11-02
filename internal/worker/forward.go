package worker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
)

// Forwarded is a port of a worker that main makes available at a port of its own.
type Forwarded struct {
	Port, Local int
}

// forwarded is a forward that a worker's connection runs, until cancel, for the sessions that asked for it.
type forwarded struct {
	local    int
	cancel   func() error
	sessions []string
}

// freePorts is how many ports Forward tries, from the worker's port up, for one that is free on main.
const freePorts = 100

// Forward makes a port on the loopback of the worker that runs a session available on main's loopback, at the same
// port or, when that is taken, at the next free one, which it returns. The forward lasts until the sessions that
// asked for it have unforwarded it or returned, or until main's connection to the worker ends.
func (manager *Manager) Forward(ctx context.Context, key string, port int) (local int, err error) {
	placement, placed := manager.options.Placements.Find(key)
	if !placed {
		return 0, refuse("the session is not on a worker")
	}
	manager.mu.Lock()
	worker := manager.workers[placement.Worker]
	var conn link
	if worker != nil {
		conn = worker.conn
		if existing := worker.forwards[port]; existing != nil {
			defer manager.mu.Unlock()
			return existing.local, manager.claim(existing, placement)
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
		return manager.addForward(worker, conn, placement, port, &forwarded{local: local, cancel: cancel})
	}
	return 0, fmt.Errorf("no port from %d to %d is free on this machine", port, local-1)
}

// addForward records a forward that a worker's connection runs for the session of a placement, unless the connection
// has ended meanwhile, the session has returned, or another Forward of the port came first.
func (manager *Manager) addForward(worker *remote, conn link, placement Placement, port int, added *forwarded) (int, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if worker.conn != conn {
		added.cancel()
		return 0, fmt.Errorf("worker %s disconnected", worker.name)
	}
	if existing := worker.forwards[port]; existing != nil {
		added.cancel()
		return existing.local, manager.claim(existing, placement)
	}
	if err := manager.claim(added, placement); err != nil {
		added.cancel()
		return 0, err
	}
	if worker.forwards == nil {
		worker.forwards = map[int]*forwarded{}
	}
	worker.forwards[port] = added
	return added.local, nil
}

// claim notes that the session of a placement asked for a forward, with manager.mu held, unless the session has
// returned since, which drops its forwards after its placement.
func (manager *Manager) claim(forward *forwarded, placement Placement) error {
	if current, placed := manager.options.Placements.Find(placement.Session); !placed || current.Move != placement.Move {
		return refuse("the session is not on a worker")
	}
	if !slices.Contains(forward.sessions, placement.Session) {
		forward.sessions = append(forward.sessions, placement.Session)
	}
	return nil
}

// Unforward stops forwarding a port of the worker that runs a session for the session, and ends the forward when no
// other session asked for it.
func (manager *Manager) Unforward(key string, port int) error {
	placement, placed := manager.options.Placements.Find(key)
	if !placed {
		return refuse("the session is not on a worker")
	}
	claimed, err := manager.unforward(placement.Worker, key, port)
	if !claimed {
		return refuse("the session doesn't forward port %d of worker %s", port, placement.Worker)
	}
	return err
}

// unforward forgets that a session on the worker of a name asked for the forwards of ports, or of all of them, ends
// those that no other session asked for, and reports whether the session had asked for one.
func (manager *Manager) unforward(name, key string, ports ...int) (claimed bool, err error) {
	manager.mu.Lock()
	var ended []func() error
	if worker := manager.workers[name]; worker != nil {
		for port, existing := range worker.forwards {
			if len(ports) > 0 && !slices.Contains(ports, port) || !slices.Contains(existing.sessions, key) {
				continue
			}
			claimed = true
			if existing.sessions = slices.DeleteFunc(existing.sessions, func(session string) bool { return session == key }); len(existing.sessions) == 0 {
				delete(worker.forwards, port)
				ended = append(ended, existing.cancel)
			}
		}
	}
	manager.mu.Unlock()
	for _, cancel := range ended {
		err = errors.Join(err, cancel())
	}
	return claimed, err
}
