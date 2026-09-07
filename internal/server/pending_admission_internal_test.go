package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/melounvitek/gripi/internal/rpc"
	"github.com/melounvitek/gripi/internal/sessions"
)

func TestPendingAdmissionFollowsRemap(t *testing.T) {
	for _, phase := range []string{"before_admission", "after_ack"} {
		for _, behavior := range []string{"", "steer", "follow_up"} {
			t.Run(phase+"/"+behavior, func(t *testing.T) {
				fixture, pending := newPendingAdmissionFixture(t)
				entered, releaseResponse, blockResponse := idleRetirementBarrier()
				defer releaseResponse()
				response := httptest.NewRecorder()
				writer := &idleRetirementResponseWriter{ResponseWriter: response, beforeWrite: blockResponse}
				var ctx context.Context = context.Background()
				beforeAdmission, releaseAdmission, blockAdmission := idleRetirementBarrier()
				defer releaseAdmission()
				if phase == "before_admission" {
					// Err is first consulted after actionSessionPath has resolved P,
					// but before the handler takes its admission lease.
					ctx = &pendingAdmissionContext{Context: ctx, beforeErr: blockAdmission}
				}
				done := idleRetirementRun(t, func() {
					fixture.handler.ServeHTTP(writer, idleRetirementRequest(ctx, pending, behavior))
				})
				if phase == "before_admission" {
					idleRetirementWait(t, beforeAdmission, "prompt before admission")
				} else {
					idleRetirementWait(t, entered, "prompt response after acknowledgement")
				}
				if err := fixture.app.remapPendingRPCClient(pending, fixture.path, pendingAdmissionClaim); err != nil {
					t.Fatal(err)
				}
				releaseAdmission()
				idleRetirementWait(t, entered, "prompt response")
				pendingAdmissionAssertProtected(t, fixture)
				releaseResponse()
				idleRetirementWait(t, done, "prompt completion")
				if response.Code != http.StatusOK {
					t.Fatalf("prompt response = %d %s", response.Code, response.Body.String())
				}
				fixture.old.assertCalls(t, behavior, 1)
				pendingAdmissionAssertReleased(t, fixture)
			})
		}
	}
}

func TestPendingAdmissionTransfersAllLeasesThroughRemapChain(t *testing.T) {
	fixture, pending := newPendingAdmissionFixture(t)
	destination := &idleRetirementClient{}
	if err := fixture.app.rpcClients.Register(fixture.path, destination); err != nil {
		t.Fatal(err)
	}
	var releases []func()
	var done []<-chan struct{}
	// Merge two source leases with an existing destination lease, then move
	// them again. Releasing any one must not expose the others to cleanup.
	for _, path := range []string{pending, pending, fixture.path} {
		entered, release, block := idleRetirementBarrier()
		defer release()
		releases = append(releases, release)
		response := httptest.NewRecorder()
		writer := &idleRetirementResponseWriter{ResponseWriter: response, beforeWrite: block}
		done = append(done, idleRetirementRun(t, func() {
			fixture.handler.ServeHTTP(writer, idleRetirementRequest(context.Background(), path, ""))
		}))
		idleRetirementWait(t, entered, "concurrent prompt response")
	}
	if err := fixture.app.remapPendingRPCClient(pending, fixture.path, pendingAdmissionClaim); err != nil {
		t.Fatal(err)
	}
	final := fixture.session(t, "final")
	if err := fixture.app.remapPendingRPCClient(fixture.path, final, pendingAdmissionClaim); err != nil {
		t.Fatal(err)
	}
	fixture.path = final
	pendingAdmissionAssertProtected(t, fixture)
	for index, release := range releases {
		release()
		idleRetirementWait(t, done[index], "one prompt completion")
		if index < len(releases)-1 {
			pendingAdmissionAssertProtected(t, fixture)
		}
	}
	fixture.old.assertCalls(t, "", 2)
	destination.assertCalls(t, "", 1)
	pendingAdmissionAssertReleased(t, fixture)
	fixture.app.promptAdmissions.mu.Lock()
	defer fixture.app.promptAdmissions.mu.Unlock()
	if len(fixture.app.promptAdmissions.sessions) != 0 {
		t.Fatal("released remapped prompts left admission entries behind")
	}
}

func TestPendingAdmissionProtectsDestinationDuringPreparation(t *testing.T) {
	fixture, pending := newPendingAdmissionFixture(t)
	entered, release, block := idleRetirementBarrier()
	defer release()
	var moveErr error
	done := idleRetirementRun(t, func() {
		moveErr = fixture.app.remapPendingRPCClient(pending, fixture.path, func() (func() error, error) {
			block()
			return nil, nil
		})
	})
	idleRetirementWait(t, entered, "remap preparation")
	checked := idleRetirementRun(t, func() {
		if finish, admitted := fixture.app.promptAdmissions.retireIdle(fixture.path); admitted {
			finish()
			t.Error("destination admitted idle cleanup during remap preparation")
		}
		// Preparation must not hold the global admission mutex.
		if finish, admitted := fixture.app.promptAdmissions.retireIdle("/unrelated"); !admitted {
			t.Error("unrelated session admission blocked")
		} else {
			finish()
		}
	})
	idleRetirementWait(t, checked, "admission checks during remap")
	release()
	idleRetirementWait(t, done, "remap completion")
	if moveErr != nil {
		t.Fatal(moveErr)
	}
	pendingAdmissionAssertReleased(t, fixture)
}

func TestPendingAdmissionRemapRejectsRetiringDestination(t *testing.T) {
	fixture, pending := newPendingAdmissionFixture(t)
	finish, admitted := fixture.app.promptAdmissions.retireIdle(fixture.path)
	if !admitted {
		t.Fatal("could not begin destination retirement")
	}
	defer finish()
	var moveErr error
	var claimed bool
	done := idleRetirementRun(t, func() {
		moveErr = fixture.app.remapPendingRPCClient(pending, fixture.path, func() (func() error, error) {
			claimed = true
			return nil, nil
		})
	})
	idleRetirementWait(t, done, "nonblocking remap rejection")
	if !errors.Is(moveErr, rpc.ErrOperationPending) || claimed {
		t.Fatalf("remap during destination retirement = %v, claimed=%v", moveErr, claimed)
	}
	if resolved, remapped := fixture.app.pendingSessions.Resolve(pending); remapped {
		t.Fatalf("rejected remap published alias %q", resolved)
	}
	if fixture.app.rpcClients.Client(pending) != fixture.old {
		t.Fatal("rejected remap changed source client")
	}
}

func TestPendingAdmissionFailedRemapKeepsSourceLeases(t *testing.T) {
	fixture, pending := newPendingAdmissionFixture(t)
	root := t.TempDir()
	fixture.app.config.AttachmentsRoot = root
	if err := os.WriteFile(filepath.Join(root, sessions.SessionHash(pending)+".jsonl"), []byte("metadata\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, sessions.SessionHash(fixture.path)+".jsonl"), 0700); err != nil {
		t.Fatal(err)
	}
	entered, release, block := idleRetirementBarrier()
	defer release()
	response := httptest.NewRecorder()
	done := idleRetirementRun(t, func() {
		fixture.handler.ServeHTTP(&idleRetirementResponseWriter{ResponseWriter: response, beforeWrite: block}, idleRetirementRequest(context.Background(), pending, ""))
	})
	idleRetirementWait(t, entered, "pending prompt response")
	rolledBack := false
	rollbackErr := errors.New("ownership rollback failed")
	err := fixture.app.remapPendingRPCClient(pending, fixture.path, func() (func() error, error) {
		return func() error { rolledBack = true; return rollbackErr }, nil
	})
	if !errors.Is(err, rollbackErr) || !rolledBack {
		t.Fatalf("failed migration = %v, ownership rolled back=%v", err, rolledBack)
	}
	if _, remapped := fixture.app.pendingSessions.Resolve(pending); remapped || fixture.app.rpcClients.Client(pending) != fixture.old {
		t.Fatal("failed migration changed source identity")
	}
	if finish, admitted := fixture.app.promptAdmissions.retireIdle(pending); admitted {
		finish()
		t.Fatal("failed remap lost the source prompt lease")
	}
	if finish, admitted := fixture.app.promptAdmissions.retireIdle(fixture.path); !admitted {
		t.Fatal("failed remap leaked destination protection")
	} else {
		finish()
	}
	release()
	idleRetirementWait(t, done, "prompt completion after failed remap")
	if finish, admitted := fixture.app.promptAdmissions.retireIdle(pending); !admitted {
		t.Fatal("source prompt lease was not released")
	} else {
		finish()
	}
	fixture.old.assertCalls(t, "", 1)
}

func newPendingAdmissionFixture(t *testing.T) (*idleRetirementFixture, string) {
	t.Helper()
	fixture := newIdleRetirementFixture(t)
	pending := filepath.Join(fixture.app.config.SessionsRoot, "pending.jsonl")
	if err := fixture.app.rpcClients.Move(fixture.path, pending); err != nil {
		t.Fatal(err)
	}
	fixture.app.pendingSessions.Remember(pending, fixture.app.config.SessionsRoot)
	return fixture, pending
}

func pendingAdmissionClaim() (func() error, error) { return nil, nil }

func pendingAdmissionAssertProtected(t *testing.T, fixture *idleRetirementFixture) {
	t.Helper()
	if err := fixture.app.cleanupIdleRPCClients(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.old.closes.Load() != 0 || fixture.app.rpcClients.Client(fixture.path) != fixture.old {
		t.Fatal("cleanup retired the remapped client while a prompt handler was active")
	}
}

func pendingAdmissionAssertReleased(t *testing.T, fixture *idleRetirementFixture) {
	t.Helper()
	if err := fixture.app.cleanupIdleRPCClients(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fixture.old.closes.Load() != 1 || fixture.app.rpcClients.Active(fixture.path) || fixture.created.Load() != 0 {
		t.Fatalf("after release: closes=%d, active=%v, factory calls=%d", fixture.old.closes.Load(), fixture.app.rpcClients.Active(fixture.path), fixture.created.Load())
	}
}

type pendingAdmissionContext struct {
	context.Context
	once      sync.Once
	beforeErr func()
}

func (ctx *pendingAdmissionContext) Err() error {
	ctx.once.Do(ctx.beforeErr)
	return ctx.Context.Err()
}
