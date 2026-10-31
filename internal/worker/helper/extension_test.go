package helper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// heldMirror holds each barrier until the test releases it.
type heldMirror struct {
	barriers chan chan error
	canceled chan struct{}
}

func (mirror *heldMirror) Barrier(ctx context.Context, session string) error {
	if session != "s1" {
		return fmt.Errorf("barrier for %s", session)
	}
	release := make(chan error)
	mirror.barriers <- release
	select {
	case err := <-release:
		return err
	case <-ctx.Done():
		close(mirror.canceled)
		return ctx.Err()
	}
}

func (mirror *heldMirror) Healthy(session string) bool { return session == "s1" }

func TestExtensionBarriersWaitForTheMirrorIndependently(t *testing.T) {
	mirror := &heldMirror{barriers: make(chan chan error), canceled: make(chan struct{})}
	extension, helper := net.Pipe()
	go answerExtension("s1", mirror, helper)
	answers := bufio.NewReader(extension)
	nextAnswer := func() string {
		t.Helper()
		extension.SetReadDeadline(time.Now().Add(2 * time.Second))
		line, err := answers.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(line)
	}

	fmt.Fprintln(extension, `{"id":1,"type":"barrier"}`)
	first := <-mirror.barriers
	fmt.Fprintln(extension, `{"id":2,"type":"barrier"}`)
	second := <-mirror.barriers
	fmt.Fprintln(extension, `{"id":3,"type":"health"}`)
	if answer := nextAnswer(); answer != `{"id":3,"ok":true}` {
		t.Fatalf("health = %s", answer)
	}
	second <- nil
	if answer := nextAnswer(); answer != `{"id":2,"ok":true}` {
		t.Fatalf("second barrier = %s", answer)
	}
	first <- errors.New("main did not acknowledge")
	if answer := nextAnswer(); answer != `{"id":1,"ok":false,"error":"main did not acknowledge"}` {
		t.Fatalf("first barrier = %s", answer)
	}

	fmt.Fprintln(extension, `{"id":4,"type":"barrier"}`)
	<-mirror.barriers
	extension.Close()
	select {
	case <-mirror.canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("the barrier of a closed connection still waits")
	}
}
