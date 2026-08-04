package server

import (
	"sync"
	"time"
)

const (
	notificationPresenceTTL        = 30 * time.Second
	notificationPresenceMaxClients = 64
)

type notificationFocusLease struct {
	path      string
	sequence  uint64
	expiresAt time.Time
}

type notificationPresence struct {
	mu      sync.Mutex
	now     func() time.Time
	clients map[string]map[string]notificationFocusLease
}

func newNotificationPresence(now func() time.Time) *notificationPresence {
	return &notificationPresence{now: now, clients: make(map[string]map[string]notificationFocusLease)}
}

func (presence *notificationPresence) Update(owner, clientID, path string, focused bool, sequence uint64) {
	presence.mu.Lock()
	defer presence.mu.Unlock()

	now := presence.now()
	presence.removeExpired(now)
	clients := presence.clients[owner]
	if lease, exists := clients[clientID]; exists && lease.sequence >= sequence {
		return
	}
	if clients == nil {
		clients = make(map[string]notificationFocusLease)
		presence.clients[owner] = clients
	}
	if _, exists := clients[clientID]; !exists && len(clients) >= notificationPresenceMaxClients {
		var oldestClient string
		var oldestExpiry time.Time
		for candidate, lease := range clients {
			if oldestClient == "" || lease.expiresAt.Before(oldestExpiry) {
				oldestClient = candidate
				oldestExpiry = lease.expiresAt
			}
		}
		delete(clients, oldestClient)
	}
	if !focused {
		path = ""
	}
	clients[clientID] = notificationFocusLease{path: path, sequence: sequence, expiresAt: now.Add(notificationPresenceTTL)}
}

func (presence *notificationPresence) Focused(owner, path string) bool {
	presence.mu.Lock()
	defer presence.mu.Unlock()

	presence.removeExpired(presence.now())
	for _, lease := range presence.clients[owner] {
		if lease.path == path {
			return true
		}
	}
	return false
}

func (presence *notificationPresence) removeExpired(now time.Time) {
	for _, clients := range presence.clients {
		for clientID, lease := range clients {
			if lease.path != "" && !lease.expiresAt.After(now) {
				lease.path = ""
				clients[clientID] = lease
			}
		}
	}
}
