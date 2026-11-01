package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/melounvitek/gripi/internal/worker/auth"
)

// fakeLoginPi stands in for main's Pi in its auth commands. It logs its arguments, prints the key in keys/<provider>,
// and refreshes a login by writing refreshed/<provider> over main's auth.json in place, as Pi does.
const fakeLoginPi = `printf '%s\n' "$*" >> "$0.log"
dir=$(dirname "$0")
case "$2" in
print-api-key) [ -f "$dir/keys/$4" ] && exec cat "$dir/keys/$4" ;;
print-bearer-token)
	if [ -f "$dir/refreshed/$4" ]; then
		sleep 0.2
		cat "$dir/refreshed/$4" > "$PI_CODING_AGENT_DIR/auth.json"
		exit 0
	fi ;;
esac
echo "Error: no login for $4" >&2
exit 1
`

// fakeWorker stands in for a worker's Control, and keeps the logins that main gives it.
type fakeWorker struct {
	mu     sync.Mutex
	logins []string
	fail   error
	held   chan struct{} // when set, the worker takes logins once it is closed, as when its Pi holds the lock on auth.json
}

func (worker *fakeWorker) ApplyAuth(_ context.Context, path, contents string) error {
	if worker.held != nil {
		<-worker.held
	}
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.fail != nil {
		return worker.fail
	}
	worker.logins = append(worker.logins, path+"\n"+contents)
	return nil
}

func (worker *fakeWorker) setFail(err error) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	worker.fail = err
}

// received returns the auth.json files that main gave the worker, each after its path on the worker.
func (worker *fakeWorker) received() []string {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return slices.Clone(worker.logins)
}

// testClock is a clock that the test sets.
type testClock struct{ now atomic.Int64 }

func (clock *testClock) Now() time.Time         { return time.UnixMilli(clock.now.Load()) }
func (clock *testClock) Set(now time.Time)      { clock.now.Store(now.UnixMilli()) }
func (clock *testClock) Add(step time.Duration) { clock.now.Add(step.Milliseconds()) }

// startLogins runs the logins of main's Pi, a fake one with an empty agent folder, checking every few milliseconds.
// calls returns the arguments of each run of Pi.
func startLogins(t *testing.T, clock *testClock) (logins *logins, dir string, calls func() []string) {
	t.Helper()
	dir = t.TempDir()
	script := filepath.Join(dir, "pi")
	writeTestFile(t, script, fakeLoginPi, 0700)
	logins = newLogins(auth.Pi{Command: []string{"/bin/sh", script}, AgentDir: filepath.Join(dir, "agent")}, clock.Now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		logins.run(ctx, 10*time.Millisecond)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return logins, dir, func() []string {
		log, _ := os.ReadFile(script + ".log")
		return strings.FieldsFunc(string(log), func(char rune) bool { return char == '\n' })
	}
}

// eventually fails the test unless done becomes true within a few seconds.
func eventually(t *testing.T, what string, done func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !done(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
	}
}

// meanwhile gives the logins time for a few checks.
func meanwhile() { time.Sleep(150 * time.Millisecond) }

func TestConnectedWorkersGetMainsLoginsWhenTheyChange(t *testing.T) {
	clock := &testClock{}
	logins, dir, _ := startLogins(t, clock)
	authJSON := filepath.Join(dir, "agent", "auth.json")
	has := func(worker *fakeWorker, text string) func() bool {
		return func() bool {
			received := worker.received()
			return len(received) > 0 && strings.Contains(received[len(received)-1], text)
		}
	}

	// Main has no logins yet.
	beelink := &fakeWorker{}
	logins.add("beelink", beelink)
	eventually(t, "the worker got main's logins", func() bool { return len(beelink.received()) > 0 })
	meanwhile()
	if received := beelink.received(); !slices.Equal(received, []string{authJSON + "\n{}"}) {
		t.Fatalf("the worker got %q", received)
	}

	writeTestFile(t, authJSON, `{"openai": {"type": "api_key", "key": "sk-1"}}`, 0600)
	eventually(t, "the worker got main's new login", has(beelink, `"sk-1"`))

	writeTestFile(t, authJSON, `{"openai": {"type": "api_key", "key": "sk-2"}}`, 0600)
	eventually(t, "the worker got main's new key", has(beelink, `"sk-2"`))
	// A key that Pi gets with a command is resolved on main.
	writeTestFile(t, filepath.Join(dir, "keys", "proxy"), "sk-proxy", 0600)
	writeTestFile(t, filepath.Join(dir, "agent", "models.json"), `{"providers": {"proxy": {"apiKey": "!cat key"}, "locked": {"apiKey": "!pass show locked"}}}`, 0600)
	eventually(t, "the worker got the proxy's key", has(beelink, `"sk-proxy"`))
	// A key that main can't resolve now, such as from a password store that is locked, is tried again a minute later.
	writeTestFile(t, filepath.Join(dir, "keys", "locked"), "sk-unlocked", 0600)
	meanwhile()
	if has(beelink, `"sk-unlocked"`)() {
		t.Fatal("main resolved every key again at once")
	}
	clock.Add(time.Minute)
	eventually(t, "the worker got the key that main could resolve later", has(beelink, `"sk-unlocked"`))

	// A worker that failed to take the logins gets them later.
	beelink.setFail(errors.New("worker: the lock on auth.json is taken"))
	writeTestFile(t, authJSON, `{"openai": {"type": "api_key", "key": "sk-3"}}`, 0600)
	meanwhile()
	beelink.setFail(nil)
	eventually(t, "the worker got the key that it failed to take", has(beelink, `"sk-3"`))

	// After a reconnect, the worker gets the logins again; the old connection gets nothing more.
	reconnected := &fakeWorker{}
	logins.remove("beelink", beelink)
	logins.add("beelink", reconnected)
	eventually(t, "the reconnected worker got main's logins", has(reconnected, `"sk-3"`))
	pushed := len(beelink.received())
	writeTestFile(t, authJSON, `{"openai": {"type": "api_key", "key": "sk-4"}}`, 0600)
	eventually(t, "the reconnected worker got main's new key", has(reconnected, `"sk-4"`))
	if len(beelink.received()) != pushed {
		t.Fatalf("the old connection got %q", beelink.received()[pushed:])
	}
	// A stale removal leaves the worker's new connection alone.
	logins.remove("beelink", beelink)
	writeTestFile(t, authJSON, `{"openai": {"type": "api_key", "key": "sk-5"}}`, 0600)
	eventually(t, "the reconnected worker got main's latest key", has(reconnected, `"sk-5"`))

	// push gives a worker the latest logins at once, as before a session starts there.
	writeTestFile(t, authJSON, `{"openai": {"type": "api_key", "key": "sk-6"}}`, 0600)
	if err := logins.push(context.Background(), "beelink"); err != nil {
		t.Fatal(err)
	}
	if !has(reconnected, `"sk-6"`)() {
		t.Fatalf("after push, the worker has %q", reconnected.received())
	}
	if err := logins.push(context.Background(), "other"); err == nil {
		t.Fatal("push to a worker that isn't connected succeeded")
	}
}

func TestAWorkerThatIsSlowToTakeLoginsHoldsUpNoOther(t *testing.T) {
	logins, dir, _ := startLogins(t, &testClock{})
	authJSON := filepath.Join(dir, "agent", "auth.json")
	writeTestFile(t, authJSON, `{"openai": {"type": "api_key", "key": "sk-1"}}`, 0600)
	slow, beelink := &fakeWorker{held: make(chan struct{})}, &fakeWorker{}
	logins.add("slow", slow)
	logins.add("beelink", beelink)
	eventually(t, "the other worker got main's logins", func() bool { return len(beelink.received()) == 1 })
	writeTestFile(t, authJSON, `{"openai": {"type": "api_key", "key": "sk-2"}}`, 0600)
	eventually(t, "the other worker got main's new key", func() bool { return len(beelink.received()) == 2 })
	// A session that is about to start there waits only as long as it may.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := logins.push(ctx, "slow"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("push to the slow worker = %v", err)
	}

	close(slow.held)
	eventually(t, "the slow worker got main's latest logins", func() bool {
		received := slow.received()
		return len(received) > 0 && strings.Contains(received[len(received)-1], `"sk-2"`)
	})
}

func TestMainRefreshesTheOAuthLoginsOfConnectedWorkersBeforeTheyExpire(t *testing.T) {
	start := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	clock := &testClock{}
	clock.Set(start)
	logins, dir, calls := startLogins(t, clock)
	writeTestFile(t, filepath.Join(dir, "agent", "auth.json"), `{"openai-codex": {"type": "oauth", "access": "access-1", "refresh": "refresh-1", "expires": `+
		milliseconds(start.Add(time.Hour))+`}}`, 0600)
	// What main's auth.json has once main's Pi has refreshed the login.
	writeTestFile(t, filepath.Join(dir, "refreshed", "openai-codex"), `{"openai-codex": {"type": "oauth", "access": "access-2", "refresh": "refresh-2", "expires": `+
		milliseconds(start.Add(2*time.Hour))+`}}`, 0600)
	refreshes := func() int {
		return len(slices.DeleteFunc(calls(), func(call string) bool { return !strings.Contains(call, "print-bearer-token") }))
	}

	// Without a worker, main's Pi refreshes its logins itself.
	clock.Set(start.Add(50 * time.Minute))
	meanwhile()
	if refreshes() != 0 {
		t.Fatalf("refreshed without a worker: %q", calls())
	}

	clock.Set(start)
	beelink := &fakeWorker{}
	logins.add("beelink", beelink)
	eventually(t, "the worker got main's login", func() bool { return len(beelink.received()) == 1 })
	clock.Set(start.Add(44 * time.Minute))
	meanwhile()
	if refreshes() != 0 {
		t.Fatalf("refreshed early: %q", calls())
	}

	// 15 minutes before the token expires, main's Pi refreshes it, once, and the worker gets the new token.
	clock.Set(start.Add(45 * time.Minute))
	eventually(t, "the worker got the refreshed token", func() bool {
		received := beelink.received()
		return strings.Contains(received[len(received)-1], `"access-2"`)
	})
	if got := calls(); !slices.Equal(got, []string{"auth print-bearer-token --provider openai-codex --min-expiry 960s"}) {
		t.Fatalf("Pi ran %q", got)
	}
	for _, received := range beelink.received() {
		if strings.Contains(received, `"refresh-1"`) || strings.Contains(received, `"refresh-2"`) || !strings.Contains(received, "gripi:refresh-on-main") {
			t.Fatalf("the worker got %s", received)
		}
	}
	clock.Set(start.Add(time.Hour))
	meanwhile()
	if refreshes() != 1 {
		t.Fatalf("refreshed the new token at once: %q", calls())
	}
}

func TestAFailedRefreshIsTriedAgainLater(t *testing.T) {
	start := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	clock := &testClock{}
	clock.Set(start)
	logins, dir, calls := startLogins(t, clock)
	// Pi can't refresh a login that an extension added.
	writeTestFile(t, filepath.Join(dir, "agent", "auth.json"), `{"extension-login": {"type": "oauth", "access": "access", "refresh": "refresh", "expires": `+
		milliseconds(start.Add(time.Hour))+`}}`, 0600)
	beelink := &fakeWorker{}
	logins.add("beelink", beelink)
	eventually(t, "the worker got main's login", func() bool { return len(beelink.received()) == 1 })

	clock.Set(start.Add(45 * time.Minute))
	eventually(t, "main tried to refresh the login", func() bool { return len(calls()) == 1 })
	// Again 30 seconds after an attempt started, and twice as long after each failure.
	for attempt, wait := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute} {
		clock.Add(wait - time.Second)
		meanwhile()
		if attempts := len(calls()); attempts != attempt+1 {
			t.Fatalf("%d attempts %s after attempt %d", attempts, wait-time.Second, attempt+1)
		}
		clock.Add(time.Second)
		eventually(t, fmt.Sprintf("attempt %d", attempt+2), func() bool { return len(calls()) == attempt+2 })
	}

	// Only while a worker is connected.
	logins.remove("beelink", beelink)
	clock.Add(time.Hour)
	meanwhile()
	if attempts := len(calls()); attempts != 4 {
		t.Fatalf("%d attempts without a worker: %q", attempts, calls())
	}
}

func milliseconds(at time.Time) string { return strconv.FormatInt(at.UnixMilli(), 10) }
