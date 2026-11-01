package worker

import (
	"context"
	"fmt"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/worker/auth"
)

// loginsPoll is how often main looks for changes to its logins and for logins due to be refreshed, while a worker is
// connected.
const loginsPoll = 3 * time.Second

// After an attempt to refresh a login starts, main tries again after refreshRetry at the earliest, and twice as long
// after each failure, up to maxRefreshRetry.
const (
	refreshRetry    = 30 * time.Second
	maxRefreshRetry = 5 * time.Minute
)

// rebuildRetry is when main builds the workers' logins again after a build that failed or left out a key, such as
// from a password store that was locked, though its files didn't change.
const rebuildRetry = time.Minute

// authApplier writes a worker's auth.json, as Control.ApplyAuth does.
type authApplier interface {
	ApplyAuth(ctx context.Context, path, contents string) error
}

// logins keeps the auth.json of every connected worker as main's logins are, without refresh tokens. Main's Pi
// refreshes each OAuth login a while before it expires, because only main may refresh it, and the workers get it
// whenever main's auth.json or models.json changes. Main's auth.json is only read here.
type logins struct {
	pi       auth.Pi
	clock    func() time.Time
	wake     chan struct{}
	building sync.Mutex // held while building, so that builds don't overtake each other
	contents string     // the last build, which failed with err; guarded by building
	err      error
	report   string // what the last build logged; guarded by building

	mu         sync.Mutex
	workers    map[string]*loginTarget
	built      string               // the stamp of main's files that the last build read, or empty after a refresh
	rebuild    time.Time            // when to build again though main's files didn't change, unless zero
	seen       map[string]seenLogin // OAuth logins, by provider
	refreshing map[string]bool
	retries    map[string]retry
}

// loginTarget is a connected worker.
type loginTarget struct {
	worker  authApplier
	pushing chan struct{} // holds a token while pushing, so that the worker gets logins in order
	pushed  string        // what the worker has, as far as main knows; guarded by pushing
}

// seenLogin is an OAuth login's expiry, and when main first saw it.
type seenLogin struct{ expires, seen time.Time }

// retry is when main may try to refresh a login again, after waiting wait.
type retry struct {
	at   time.Time
	wait time.Duration
}

func newLogins(pi auth.Pi, clock func() time.Time) *logins {
	return &logins{
		pi: pi, clock: clock, wake: make(chan struct{}, 1),
		workers: map[string]*loginTarget{}, seen: map[string]seenLogin{}, refreshing: map[string]bool{}, retries: map[string]retry{},
	}
}

// run checks main's logins every interval, and after each refresh, until ctx ends.
func (logins *logins) run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-logins.wake:
		}
		logins.check(ctx)
	}
}

// add keeps the logins of a worker that main connected to current, until remove.
func (logins *logins) add(name string, worker authApplier) {
	logins.mu.Lock()
	defer logins.mu.Unlock()
	logins.workers[name] = &loginTarget{worker: worker, pushing: make(chan struct{}, 1)}
}

// remove forgets a worker's connection, unless the worker has another one by now.
func (logins *logins) remove(name string, worker authApplier) {
	logins.mu.Lock()
	defer logins.mu.Unlock()
	if target := logins.workers[name]; target != nil && target.worker == worker {
		delete(logins.workers, name)
	}
}

// check refreshes the logins that are due and gives every connected worker the latest logins. A worker whose Pi holds
// the lock on its auth.json for long holds up no other.
func (logins *logins) check(ctx context.Context) {
	logins.mu.Lock()
	targets := maps.Clone(logins.workers)
	logins.mu.Unlock()
	if len(targets) == 0 {
		return
	}
	if _, err := logins.build(ctx); err != nil {
		return
	}
	logins.refreshDue()
	for name, target := range targets {
		select {
		case target.pushing <- struct{}{}:
		default:
			// The last push there is still going.
			continue
		}
		go func() {
			defer func() { <-target.pushing }()
			if err := logins.pushTo(ctx, target); err != nil && ctx.Err() == nil {
				log.Printf("give worker %s this machine's logins: %v", name, err)
			}
		}()
	}
}

// push gives a connected worker the latest logins, unless it has them already.
func (logins *logins) push(ctx context.Context, name string) error {
	logins.mu.Lock()
	target := logins.workers[name]
	logins.mu.Unlock()
	if target == nil {
		return fmt.Errorf("worker %s disconnected", name)
	}
	select {
	case target.pushing <- struct{}{}:
		defer func() { <-target.pushing }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return logins.pushTo(ctx, target)
}

// pushTo is push with target.pushing held.
func (logins *logins) pushTo(ctx context.Context, target *loginTarget) error {
	contents, err := logins.build(ctx)
	if err != nil || contents == target.pushed {
		return err
	}
	if err := target.worker.ApplyAuth(ctx, filepath.Join(logins.pi.AgentDir, "auth.json"), contents); err != nil {
		return err
	}
	target.pushed = contents
	return nil
}

// build returns the workers' auth.json, which it builds again only when main's auth.json or models.json changed, after
// a refresh, or a while after a build that failed or left out a key.
func (logins *logins) build(ctx context.Context) (string, error) {
	logins.building.Lock()
	defer logins.building.Unlock()
	stamp := ""
	for _, name := range []string{"auth.json", "models.json"} {
		stamp += name
		if info, err := os.Stat(filepath.Join(logins.pi.AgentDir, name)); err == nil {
			stamp += fmt.Sprintf(" %d %d", info.Size(), info.ModTime().UnixNano())
		}
		stamp += "\n"
	}
	logins.mu.Lock()
	cached := stamp == logins.built && (logins.rebuild.IsZero() || logins.clock().Before(logins.rebuild))
	logins.mu.Unlock()
	if cached {
		return logins.contents, logins.err
	}

	result, err := logins.pi.Build(ctx)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	report := ""
	if err != nil {
		report = fmt.Sprintf("make the workers' logins from this machine's: %v\n", err)
	}
	for _, provider := range slices.Sorted(maps.Keys(result.Skipped)) {
		report += fmt.Sprintf("Warning: workers get no key for %s, because this machine could not resolve it: %v\n", provider, result.Skipped[provider])
	}
	// A failure that lasts is logged once.
	if report != "" && report != logins.report {
		log.Print(report)
	}
	logins.report = report
	logins.mu.Lock()
	defer logins.mu.Unlock()
	logins.built, logins.err, logins.rebuild = stamp, err, time.Time{}
	if report != "" {
		logins.rebuild = logins.clock().Add(rebuildRetry)
	}
	if err != nil {
		return "", err
	}
	logins.contents = string(result.Contents)
	now := logins.clock()
	for provider, expires := range result.Expires {
		if login, found := logins.seen[provider]; !found || !login.expires.Equal(expires) {
			logins.seen[provider] = seenLogin{expires: expires, seen: now}
		}
	}
	maps.DeleteFunc(logins.seen, func(provider string, _ seenLogin) bool { _, found := result.Expires[provider]; return !found })
	return logins.contents, nil
}

// refreshDue has main's Pi refresh each OAuth login that is due, in the background and one at a time for each
// provider, because a refresh can take about 30 seconds.
func (logins *logins) refreshDue() {
	now := logins.clock()
	logins.mu.Lock()
	defer logins.mu.Unlock()
	for provider, login := range logins.seen {
		if logins.refreshing[provider] || now.Before(auth.RefreshAt(login.seen, login.expires)) || now.Before(logins.retries[provider].at) {
			continue
		}
		logins.refreshing[provider] = true
		go logins.refresh(provider, login, now)
	}
}

func (logins *logins) refresh(provider string, login seenLogin, started time.Time) {
	err := logins.pi.Refresh(provider, login.seen, login.expires)
	logins.mu.Lock()
	delete(logins.refreshing, provider)
	retry := logins.retries[provider]
	if err != nil {
		retry.wait = min(max(2*retry.wait, refreshRetry), maxRefreshRetry)
	} else {
		retry.wait = 0
		// Pi may have rewritten main's auth.json too quickly for its stamp to change.
		logins.built = ""
	}
	retry.at = started.Add(max(retry.wait, refreshRetry))
	logins.retries[provider] = retry
	logins.mu.Unlock()
	if err != nil {
		log.Printf("refresh the %s login of this machine's Pi for workers, whose token expires at %s: %v; trying again in %s",
			provider, login.expires.Local().Format(time.TimeOnly), err, retry.wait)
		return
	}
	select {
	case logins.wake <- struct{}{}:
	default:
	}
}
