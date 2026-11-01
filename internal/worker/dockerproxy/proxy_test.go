package dockerproxy_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/dockerproxy"
)

const (
	from = "/home/vit"
	to   = "/home/worker"
)

// received is what the fake Docker daemon got.
type received struct {
	method, path string
	body         []byte
}

// startProxy serves a proxy in front of a fake Docker daemon on a Unix socket, and returns a client of the proxy.
func startProxy(t *testing.T, daemon http.Handler) (*http.Client, string) {
	t.Helper()
	return startProxyFor(t, daemon, from, to)
}

func startProxyFor(t *testing.T, daemon http.Handler, from, to string) (*http.Client, string) {
	t.Helper()
	return startProxyWith(t, daemon, dockerproxy.Mapping{From: from, To: to})
}

func startProxyWith(t *testing.T, daemon http.Handler, mappings ...dockerproxy.Mapping) (*http.Client, string) {
	t.Helper()
	dir := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(dir, "daemon.sock"))
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewUnstartedServer(daemon)
	upstream.Listener = listener
	upstream.Start()
	t.Cleanup(upstream.Close)
	return serve(t, dockerproxy.Options{Listen: filepath.Join(dir, "proxy.sock"), Upstream: "unix://" + listener.Addr().String(), Mappings: mappings})
}

func serve(t *testing.T, options dockerproxy.Options) (*http.Client, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dockerproxy.Serve(ctx, options) }()
	for start := time.Now(); ; time.Sleep(10 * time.Millisecond) {
		if _, err := os.Stat(options.Listen); err == nil {
			break
		}
		select {
		case err := <-done:
			cancel()
			t.Fatalf("Serve: %v", err)
		default:
		}
		if time.Since(start) > 5*time.Second {
			cancel()
			t.Fatal("the proxy's socket didn't appear")
		}
	}
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != context.Canceled {
			t.Errorf("Serve returned %v, want context.Canceled", err)
		}
	})
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, "unix", options.Listen)
	}}}
	return client, options.Listen
}

// recorder is a fake daemon that answers every request with status 201 and an empty object, and passes what it got to the channel.
func recorder() (http.Handler, chan received) {
	requests := make(chan received, 1)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- received{r.Method, r.URL.Path, body}
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "{}")
	}), requests
}

func post(t *testing.T, client *http.Client, path, body string) {
	t.Helper()
	if status, message := send(t, client, path, body); status != http.StatusCreated {
		t.Fatalf("POST %s: status %d, %q", path, status, message)
	}
}

// send posts body, and returns the answer's status and message.
func send(t *testing.T, client *http.Client, path, body string) (int, string) {
	t.Helper()
	res, err := client.Post("http://docker"+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var answer struct{ Message string }
	json.NewDecoder(res.Body).Decode(&answer)
	return res.StatusCode, answer.Message
}

// refuse checks that the proxy refuses the request with a message that contains want, and doesn't pass it on.
func refuse(t *testing.T, client *http.Client, requests chan received, path, body, want string) {
	t.Helper()
	if status, message := send(t, client, path, body); status != http.StatusBadRequest || !strings.Contains(message, want) {
		t.Errorf("POST %s %s: status %d, %q, want 400 with %q", path, body, status, message, want)
	}
	select {
	case got := <-requests:
		t.Errorf("POST %s %s: the daemon got %s", path, body, got.body)
	default:
	}
}

// expectReceived checks that the daemon got body as want.
func expectReceived(t *testing.T, requests chan received, body, want string) {
	t.Helper()
	if got := <-requests; !reflect.DeepEqual(decode(t, got.body), decode(t, []byte(want))) {
		t.Errorf("for %s, the daemon got\n%s\nwant\n%s", body, got.body, want)
	}
}

func decode(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return value
}

func TestRewritesFoldersInRequests(t *testing.T) {
	container := `{
		"Image": "alpine", "Cmd": ["cat", "/home/vit/file"], "Memory": 9007199254740993, "Extra": {"Nested": [1, "/home/vit/x", null]},
		"HostConfig": {
			"Binds": ["/home/vit/proj:/x:ro", "/home/vit:/home", "/home/vitek/proj:/y", "/srv/data:/z", "named:/home/vit/v", "/home/vit/anonymous"],
			"Mounts": [
				{"Type": "bind", "Source": "/home/vit/proj", "Target": "/m", "ReadOnly": true, "BindOptions": {"Propagation": "rprivate"}},
				{"Type": "bind", "Source": "/home/vitek", "Target": "/n"},
				{"Type": "volume", "Source": "data", "Target": "/d"},
				{"Type": "volume", "Target": "/e", "VolumeOptions": {"DriverConfig": {"Name": "local", "Options": {"type": "none", "o": "bind", "device": "/home/vit/data"}}}},
				{"Type": "tmpfs", "Target": "/t"}
			],
			"Privileged": false, "Future": {"Field": "/home/vit/z"}
		}
	}`
	wantContainer := `{
		"Image": "alpine", "Cmd": ["cat", "/home/vit/file"], "Memory": 9007199254740993, "Extra": {"Nested": [1, "/home/vit/x", null]},
		"HostConfig": {
			"Binds": ["/home/worker/proj:/x:ro", "/home/worker:/home", "/home/vitek/proj:/y", "/srv/data:/z", "named:/home/vit/v", "/home/vit/anonymous"],
			"Mounts": [
				{"Type": "bind", "Source": "/home/worker/proj", "Target": "/m", "ReadOnly": true, "BindOptions": {"Propagation": "rprivate"}},
				{"Type": "bind", "Source": "/home/vitek", "Target": "/n"},
				{"Type": "volume", "Source": "data", "Target": "/d"},
				{"Type": "volume", "Target": "/e", "VolumeOptions": {"DriverConfig": {"Name": "local", "Options": {"type": "none", "o": "bind", "device": "/home/worker/data"}}}},
				{"Type": "tmpfs", "Target": "/t"}
			],
			"Privileged": false, "Future": {"Field": "/home/vit/z"}
		}
	}`
	volume := `{"Name": "data", "Driver": "local", "DriverOpts": {"type": "none", "o": "bind", "device": "/home/vit/data"}, "Labels": {"a": "b"}}`
	wantVolume := `{"Name": "data", "Driver": "local", "DriverOpts": {"type": "none", "o": "bind", "device": "/home/worker/data"}, "Labels": {"a": "b"}}`
	for _, test := range []struct{ path, body, want string }{
		{"/v1.52/containers/create?name=app", container, wantContainer},
		{"/containers/create", container, wantContainer},
		{"/v1.24/volumes/create", volume, wantVolume},
		{"/volumes/create", volume, wantVolume},
	} {
		t.Run(test.path, func(t *testing.T) {
			daemon, requests := recorder()
			client, _ := startProxy(t, daemon)
			post(t, client, test.path, test.body)
			got := <-requests
			if want := strings.Split(test.path, "?")[0]; got.path != want {
				t.Errorf("the daemon got %s, want %s", got.path, want)
			}
			if !reflect.DeepEqual(decode(t, got.body), decode(t, []byte(test.want))) {
				t.Errorf("the daemon got\n%s\nwant\n%s", got.body, test.want)
			}
		})
	}
}

// A moved session's view shows its copy at main's folders in main's home folder, so a path reaches Docker by the
// longest folder that holds it, once: those folders to the copy, the rest of main's home to the worker user's.
func TestMapsEachPathByTheLongestFolderThatHoldsIt(t *testing.T) {
	const copy = "/home/worker/.local/share/gripi/worker/copies/m1"
	mappings := []dockerproxy.Mapping{{From: from, To: to}, {From: from + "/Work", To: copy + "/Work"}}
	daemon, requests := recorder()
	client, _ := startProxyWith(t, daemon, mappings...)
	for path, want := range map[string]string{
		from + "/Work/gripi": copy + "/Work/gripi", from + "/Work": copy + "/Work", from + "/Workshop": to + "/Workshop",
		from + "/.cache": to + "/.cache", "/srv/data": "/srv/data",
	} {
		for _, request := range hostFolderRequests {
			body := fmt.Sprintf(request.body, path)
			post(t, client, request.path, body)
			expectReceived(t, requests, body, fmt.Sprintf(request.body, want))
		}
	}

	answers := strings.NewReplacer("COPY", copy, "TO", to)
	inspected := answers.Replace(`{"Mounts": [{"Source": "COPY/Work/gripi"}, {"Source": "TO/.cache"}, {"Source": "TO/.local/share/gripi/worker/copies/m2/Work"}]}`)
	client, _ = startProxyWith(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, inspected) }), mappings...)
	res, err := client.Get("http://docker/v1.52/containers/abc/json")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	want := `{"Mounts": [{"Source": "/home/vit/Work/gripi"}, {"Source": "/home/vit/.cache"}, {"Source": "/home/vit/.local/share/gripi/worker/copies/m2/Work"}]}`
	if !reflect.DeepEqual(decode(t, body), decode(t, []byte(want))) {
		t.Errorf("the inspection shows\n%s\nwant\n%s", body, want)
	}
}

func TestPassesOtherRequestsUnchanged(t *testing.T) {
	for _, test := range []struct{ path, body string }{
		{"/v1.52/containers/create", `{"Image":"alpine",  "HostConfig": {"Binds": ["/home/vitek:/x", "data:/home/vit"], "Mounts": [{"Type": "bind", "Source": "/srv/x"}]}}`},
		{"/v1.52/containers/abc/update", `{"HostConfig": {"Binds": ["/home/vit:/x"]}}`},
		{"/v1.52/volumes/create", `{"Name": "x", "DriverOpts": {"device": "/home/vitek"}}`},
		{"/v1.52/volumes/create", `{"Name": "x", "DriverOpts": {"type": "cifs", "o": "addr=server", "device": "//server/share"}}`},
		{"/v1.52/containers/create", `not JSON /home/vit:/x`},
	} {
		daemon, requests := recorder()
		client, _ := startProxy(t, daemon)
		post(t, client, test.path, test.body)
		if got := <-requests; string(got.body) != test.body {
			t.Errorf("POST %s: the daemon got\n%s\nwant\n%s", test.path, got.body, test.body)
		}
	}
}

// hostFolderRequests name a host folder, %s, in each place where the proxy looks for one.
var hostFolderRequests = []struct{ path, body string }{
	{"/v1.52/containers/create", `{"HostConfig": {"Binds": ["%s:/x:ro"]}}`},
	{"/v1.52/containers/create", `{"HostConfig": {"Mounts": [{"Type": "bind", "Source": "%s", "Target": "/x"}]}}`},
	{"/v1.52/containers/create", `{"HostConfig": {"Mounts": [{"Type": "volume", "Target": "/x", "VolumeOptions": {"DriverConfig": {"Name": "local", "Options": {"type": "none", "o": "bind", "device": "%s"}}}}]}}`},
	{"/v1.52/volumes/create", `{"Name": "x", "DriverOpts": {"type": "none", "o": "rbind", "device": "%s"}}`},
}

// The proxy runs in the view, so it follows symbolic links as the view shows them, where the daemon would follow them
// outside it, and main's home here holds what the view shows there.
func TestResolvesHostFoldersAsTheViewShowsThem(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home", "vit")
	outside := filepath.Join(root, "srv")
	for _, dir := range []string{home + "/project", home + "/shared", outside} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		home + "/project/absolute": home + "/shared",
		home + "/project/relative": "../shared",
		home + "/project/outside":  outside,
		outside + "/inside":        home + "/project",
		home + "/project/missing":  home + "/nowhere",
		home + "/project/loop":     "loop",
		root + "/alias":            home,
	} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}
	daemon, requests := recorder()
	client, _ := startProxyFor(t, daemon, home, to)
	// Main's home itself may have a symbolic link in its path.
	aliasClient, _ := startProxyFor(t, daemon, root+"/alias", to)
	for _, test := range []struct {
		client     *http.Client
		path, want string
	}{
		{client, home + "/project", to + "/project"},
		{client, home, to},
		{client, home + "/project/absolute", to + "/shared"},
		{client, home + "/project/relative/file", to + "/shared/file"},
		{client, home + "/project/absolute/new/folder", to + "/shared/new/folder"},
		{client, home + "/new/folder", to + "/new/folder"},
		{client, home + "/project/../shared", to + "/shared"},
		{client, root + "/home/./vit/project/", to + "/project"},
		{client, home + "/../vit//project", to + "/project"},
		// Docker cleans a bind's path before it follows links.
		{client, home + "/project/absolute/..", to + "/project"},
		{client, home + "/project/outside", outside},
		{client, outside + "/inside", to + "/project"},
		{client, home + "/..", root + "/home"},
		{client, "/gripi-missing/folder", "/gripi-missing/folder"},
		{aliasClient, root + "/alias/project/absolute", to + "/shared"},
		{aliasClient, home + "/project", to + "/project"},
	} {
		for _, request := range hostFolderRequests {
			body := fmt.Sprintf(request.body, test.path)
			post(t, test.client, request.path, body)
			expectReceived(t, requests, body, fmt.Sprintf(request.body, test.want))
		}
	}
	// These can't be followed in the view, while the daemon might follow them to another user's files outside it.
	for _, path := range []string{home + "/project/missing", home + "/project/missing/file", home + "/project/loop"} {
		for _, request := range hostFolderRequests {
			refuse(t, client, requests, request.path, fmt.Sprintf(request.body, path), "can't resolve "+path)
		}
	}
}

// Docker's decoder matches struct fields to keys in any case.
func TestFindsFieldsInAnyCase(t *testing.T) {
	for _, test := range []struct{ path, body, want string }{
		{
			"/v1.52/containers/create",
			`{"Image": "alpine", "hostconfig": {"binds": ["/home/vit/project:/data"]}}`,
			`{"Image": "alpine", "hostconfig": {"binds": ["/home/worker/project:/data"]}}`,
		},
		{
			"/v1.52/containers/create",
			`{"HOSTCONFIG": {"BINDS": ["/home/vit/b:/b"], "mounts": [{"type": "bind", "source": "/home/vit/m"}, {"SOURCE": "/home/vit/n"},
				{"volumeoptions": {"driverconfig": {"options": {"o": "bind", "device": "/home/vit/d"}}}},
				{"VolumeOptions": {"DRIVERCONFIG": {"Options": {"o": "bind", "Device": "/home/vit/d"}}}}]}}`,
			`{"HOSTCONFIG": {"BINDS": ["/home/worker/b:/b"], "mounts": [{"type": "bind", "source": "/home/worker/m"}, {"SOURCE": "/home/worker/n"},
				{"volumeoptions": {"driverconfig": {"options": {"o": "bind", "device": "/home/worker/d"}}}},
				{"VolumeOptions": {"DRIVERCONFIG": {"Options": {"o": "bind", "Device": "/home/vit/d"}}}}]}}`,
		},
		// Go folds ſ to s, as Unicode does.
		{
			"/v1.52/containers/create",
			`{"hostConfig": {"Mounts": [{"Type": "bind", "ſource": "/home/vit/m"}]}}`,
			`{"hostConfig": {"Mounts": [{"Type": "bind", "ſource": "/home/worker/m"}]}}`,
		},
		{
			"/v1.52/volumes/create",
			`{"name": "x", "driveropts": {"o": "bind", "device": "/home/vit/d"}}`,
			`{"name": "x", "driveropts": {"o": "bind", "device": "/home/worker/d"}}`,
		},
		// Docker reads the names of options as they are.
		{
			"/v1.52/volumes/create",
			`{"DriverOpts": {"o": "bind", "Device": "/home/vit/d"}}`,
			`{"DriverOpts": {"o": "bind", "Device": "/home/vit/d"}}`,
		},
	} {
		daemon, requests := recorder()
		client, _ := startProxy(t, daemon)
		post(t, client, test.path, test.body)
		expectReceived(t, requests, test.body, test.want)
	}
}

func TestRefusesAmbiguousFields(t *testing.T) {
	daemon, requests := recorder()
	client, _ := startProxy(t, daemon)
	for _, test := range []struct{ path, body, want string }{
		{"/v1.52/containers/create", `{"HostConfig": {"Binds": ["/srv:/x"]}, "hostconfig": {"Binds": ["/home/vit:/x"]}}`, "HostConfig"},
		{"/v1.52/containers/create", `{"HostConfig": {"Binds": ["/srv:/x"], "BINDS": ["/home/vit:/x"]}}`, "Binds"},
		{"/v1.52/containers/create", `{"HostConfig": {"Mounts": [{"Type": "bind", "Source": "/srv", "source": "/home/vit"}]}}`, "Source"},
		{"/v1.52/volumes/create", `{"DriverOpts": {"o": "bind", "device": "/home/vit"}, "driverOpts": {}}`, "DriverOpts"},
		// Docker merges objects under a repeated key.
		{"/v1.52/containers/create", `{"HostConfig": {"Binds": ["/home/vit:/x"]}, "HostConfig": {"Privileged": false}}`, "HostConfig"},
		{"/v1.52/volumes/create", `{"DriverOpts": {"o": "bind", "device": "/home/vit"}, "DriverOpts": {"type": "none"}}`, "DriverOpts"},
	} {
		refuse(t, client, requests, test.path, test.body, test.want)
	}
}

func TestMapsFoldersBackInInspections(t *testing.T) {
	answers := map[string]string{
		"/v1.52/containers/abc/json": `{"Id": "abc", "Unknown": 9007199254740993,
			"HostConfig": {"Binds": ["/home/worker/proj:/x", "/home/workers:/y"], "Mounts": [{"Type": "bind", "Source": "/home/worker/m", "Target": "/m"}]},
			"Mounts": [{"Type": "bind", "Source": "/home/worker/proj", "Destination": "/x"}, {"Type": "volume", "Name": "data", "Source": "/var/lib/docker/volumes/data/_data"}]}`,
		"/volumes/data":     `{"Name": "data", "Mountpoint": "/var/lib/docker/volumes/data/_data", "Options": {"device": "/home/worker/data", "o": "bind"}}`,
		"/volumes/rootless": `{"Name": "rootless", "Mountpoint": "/home/worker/.local/share/docker/volumes/rootless/_data"}`,
	}
	want := map[string]string{
		"/v1.52/containers/abc/json": `{"Id": "abc", "Unknown": 9007199254740993,
			"HostConfig": {"Binds": ["/home/vit/proj:/x", "/home/workers:/y"], "Mounts": [{"Type": "bind", "Source": "/home/vit/m", "Target": "/m"}]},
			"Mounts": [{"Type": "bind", "Source": "/home/vit/proj", "Destination": "/x"}, {"Type": "volume", "Name": "data", "Source": "/var/lib/docker/volumes/data/_data"}]}`,
		"/volumes/data":     `{"Name": "data", "Mountpoint": "/var/lib/docker/volumes/data/_data", "Options": {"device": "/home/vit/data", "o": "bind"}}`,
		"/volumes/rootless": `{"Name": "rootless", "Mountpoint": "/home/vit/.local/share/docker/volumes/rootless/_data"}`,
	}
	client, _ := startProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, answers[r.URL.Path])
	}))
	for path := range answers {
		res, err := client.Get("http://docker" + path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if length := res.Header.Get("Content-Length"); length != strconv.Itoa(len(body)) {
			t.Errorf("GET %s: Content-Length %s for %d bytes", path, length, len(body))
		}
		if !reflect.DeepEqual(decode(t, body), decode(t, []byte(want[path]))) {
			t.Errorf("GET %s: got\n%s\nwant\n%s", path, body, want[path])
		}
	}
}

func TestStreamsWithoutBuffering(t *testing.T) {
	release := make(chan struct{})
	client, _ := startProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "first\n")
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		io.WriteString(w, "second\n")
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/v1.52/events", nil)
	res, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	reader := bufio.NewReader(res.Body)
	if line, err := reader.ReadString('\n'); line != "first\n" {
		t.Fatalf("got %q, %v before the stream ended", line, err)
	}
	close(release)
	if rest, err := io.ReadAll(reader); string(rest) != "second\n" || err != nil {
		t.Fatalf("got %q, %v at the end", rest, err)
	}
}

// The daemon answers attach and exec start with 101, and then both sides send raw bytes; the client half-closes when its input ends.
func TestPassesUpgradedConnections(t *testing.T) {
	_, socket := startProxy(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		conn, buffered, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		io.WriteString(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\nready\n")
		input, _ := io.ReadAll(buffered)
		fmt.Fprintf(conn, "%s %s got %s", r.URL.Path, body, input)
	}))
	conn, err := net.Dial("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	body := `{"Detach":false,"Tty":false}`
	fmt.Fprintf(conn, "POST /v1.52/exec/abc/start HTTP/1.1\r\nHost: docker\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", len(body), body)
	reader := bufio.NewReader(conn)
	res, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusSwitchingProtocols || res.Header.Get("Upgrade") != "tcp" {
		t.Fatalf("got %s with Upgrade %q", res.Status, res.Header.Get("Upgrade"))
	}
	if line, err := reader.ReadString('\n'); line != "ready\n" {
		t.Fatalf("got %q, %v", line, err)
	}
	io.WriteString(conn, "input\x00\xff")
	conn.(*net.UnixConn).CloseWrite()
	rest, err := io.ReadAll(reader)
	if want := "/v1.52/exec/abc/start " + body + " got input\x00\xff"; string(rest) != want || err != nil {
		t.Fatalf("got %q, %v, want %q", rest, err, want)
	}
}

func TestServesPrivateSocketForTCPEndpoints(t *testing.T) {
	daemon, requests := recorder()
	upstream := httptest.NewServer(daemon)
	defer upstream.Close()
	client, socket := serve(t, dockerproxy.Options{
		Listen: filepath.Join(t.TempDir(), "proxy.sock"), Upstream: "tcp://" + upstream.Listener.Addr().String(), Mappings: []dockerproxy.Mapping{{From: from, To: to}},
	})
	if info, err := os.Stat(socket); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("socket: %v, %v", info.Mode(), err)
	}
	post(t, client, "/v1.52/volumes/create", `{"DriverOpts": {"o": "bind", "device": "/home/vit/x"}}`)
	if got := <-requests; !reflect.DeepEqual(decode(t, got.body), decode(t, []byte(`{"DriverOpts":{"o":"bind","device":"/home/worker/x"}}`))) {
		t.Errorf("the daemon got %s", got.body)
	}
}

func TestRefusesUnsupportedEndpoints(t *testing.T) {
	for _, upstream := range []string{"ssh://user@host", "npipe:////./pipe/docker_engine", ""} {
		err := dockerproxy.Serve(context.Background(), dockerproxy.Options{
			Listen: filepath.Join(t.TempDir(), "proxy.sock"), Upstream: upstream, Mappings: []dockerproxy.Mapping{{From: from, To: to}},
		})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%q", upstream)) {
			t.Errorf("Serve with %q: %v", upstream, err)
		}
	}
}

func TestUpstream(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin)
	if got := dockerproxy.Upstream([]string{"DOCKER_CONTEXT=remote"}); got != "unix:///var/run/docker.sock" {
		t.Errorf("without docker: %s", got)
	}
	script := "#!/bin/sh\n[ \"$*\" = 'context inspect --format {{.Endpoints.docker.Host}}' ] && echo \"unix:///run/$DOCKER_CONTEXT.sock\"\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if got := dockerproxy.Upstream([]string{"DOCKER_CONTEXT=remote"}); got != "unix:///run/remote.sock" {
		t.Errorf("from the context: %s", got)
	}
	if got := dockerproxy.Upstream([]string{"DOCKER_CONTEXT=remote", "DOCKER_HOST=tcp://docker:2375"}); got != "tcp://docker:2375" {
		t.Errorf("from DOCKER_HOST: %s", got)
	}
}
