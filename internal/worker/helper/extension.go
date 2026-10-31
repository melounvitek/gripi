package helper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
)

// Mirror answers Gripi's worker extension, which holds every tool result of a session until main has its changes.
type Mirror interface {
	// Barrier returns nil once main has applied every change made so far to the session's repository.
	Barrier(ctx context.Context, session string) error
	// Healthy reports whether the session's repository is mirrored to main.
	Healthy(session string) bool
}

// serveExtension answers the worker extension on the socket named by GRIPI_WORKER_SOCKET, which identifies the
// session. Requests are newline-delimited JSON: {"id":1,"type":"barrier"} gets {"id":1,"ok":true} or
// {"id":1,"ok":false,"error":"…"}, and {"id":2,"type":"health"} gets {"id":2,"ok":true|false}. Without a mirror,
// barriers fail and the session is not healthy, so the extension fails closed.
func (s *server) serveExtension(id string, listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go answerExtension(id, s.options.Mirror, connection)
	}
}

func answerExtension(id string, mirror Mirror, connection net.Conn) {
	defer connection.Close()
	// Nobody waits for barriers of an extension that went away.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var writing sync.Mutex
	encoder := json.NewEncoder(connection)
	decoder := json.NewDecoder(connection)
	for {
		var request Message
		if decoder.Decode(&request) != nil {
			return
		}
		// Parallel tool calls wait for barriers of their own.
		go func() {
			var answer Response
			switch request.Type {
			case "barrier":
				err := errors.New("nothing mirrors this session to main")
				if mirror != nil {
					err = mirror.Barrier(ctx, id)
				}
				answer = response(request.ID, err)
			case "health":
				answer = Response{ID: request.ID, OK: mirror != nil && mirror.Healthy(id)}
			default:
				answer = response(request.ID, fmt.Errorf("unknown request %q", request.Type))
			}
			writing.Lock()
			defer writing.Unlock()
			encoder.Encode(answer)
		}()
	}
}
