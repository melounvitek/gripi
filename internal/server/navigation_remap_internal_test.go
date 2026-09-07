package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestNavigationPendingRemapHTTPConflict(t *testing.T) {
	fixture := newTreeNavigationFixture(t)
	from, to := fixture.path, fixture.session(t, "native")
	fixture.app.pendingSessions.Remember(from, fixture.app.config.SessionsRoot)
	entered, release, block := idleRetirementBarrier()
	defer release()
	response := httptest.NewRecorder()
	done := idleRetirementRun(t, func() {
		fixture.handler.ServeHTTP(&idleRetirementResponseWriter{ResponseWriter: response, beforeWrite: block}, treeNavigationRequest(context.Background(), from))
	})
	idleRetirementWait(t, entered, "navigation response")
	// Native Pi now reports its persisted path. The next HTTP request must
	// attempt a real remap before it reaches prompt admission.
	fixture.a.state = map[string]any{"success": true, "data": map[string]any{"sessionFile": to, "isStreaming": false, "isCompacting": false}}
	conflict := treeNavigationServe(t, fixture.handler, idleRetirementRequest(context.Background(), from, ""))
	treeNavigationAssertConflict(t, conflict)
	var payload map[string]any
	if err := json.Unmarshal(conflict.Body.Bytes(), &payload); err != nil || payload["retryable"] != false {
		t.Errorf("navigation conflict must not be retried: %s", conflict.Body.String())
	}
	if _, remapped := fixture.app.pendingSessions.Resolve(from); remapped || fixture.app.rpcClients.Client(from) != fixture.a || fixture.app.rpcClients.Active(to) {
		t.Fatal("conflicting HTTP request changed pending session identity")
	}
	fixture.assertNoPrompts(t)
	release()
	idleRetirementWait(t, done, "navigation completion")
	treeNavigationAssertSuccess(t, response, from, "branch draft")
	prompt := treeNavigationServe(t, fixture.handler, idleRetirementRequest(context.Background(), from, ""))
	idleRetirementAssertSuccess(t, prompt, to, "")
	if resolved, remapped := fixture.app.pendingSessions.Resolve(from); !remapped || resolved != to || fixture.app.rpcClients.Client(to) != fixture.a {
		t.Fatal("HTTP request did not remap the pending client after navigation released")
	}
	navigation := treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), from))
	treeNavigationAssertSuccess(t, navigation, to, "branch draft")
}

func TestNavigationPreventsPendingRemap(t *testing.T) {
	for _, protected := range []string{"source", "destination"} {
		t.Run(protected, func(t *testing.T) {
			fixture := newTreeNavigationFixture(t)
			from, to := fixture.path, fixture.session(t, "native")
			fixture.app.pendingSessions.Remember(from, fixture.app.config.SessionsRoot)
			path := from
			if protected == "destination" {
				path = to
				if err := fixture.app.rpcClients.Register(to, fixture.b); err != nil {
					t.Fatal(err)
				}
			}
			entered, release, block := idleRetirementBarrier()
			defer release()
			response := httptest.NewRecorder()
			done := idleRetirementRun(t, func() {
				fixture.handler.ServeHTTP(&idleRetirementResponseWriter{ResponseWriter: response, beforeWrite: block}, treeNavigationRequest(context.Background(), path))
			})
			idleRetirementWait(t, entered, "navigation response")
			destination := fixture.app.rpcClients.Client(to)
			claimed := false
			var moveErr error
			attempted := idleRetirementRun(t, func() {
				moveErr = fixture.app.remapPendingRPCClient(from, to, func() (func() error, error) {
					claimed = true
					return nil, nil
				})
			})
			idleRetirementWait(t, attempted, "remap rejection during navigation")
			if moveErr == nil || claimed {
				t.Fatalf("remap during %s navigation = %v, claimed=%v", protected, moveErr, claimed)
			}
			if _, remapped := fixture.app.pendingSessions.Resolve(from); remapped || fixture.app.rpcClients.Client(from) != fixture.a || fixture.app.rpcClients.Client(to) != destination {
				t.Fatal("rejected remap changed the alias or either client")
			}
			release()
			idleRetirementWait(t, done, "navigation completion")
			treeNavigationAssertSuccess(t, response, path, "branch draft")
			if err := fixture.app.remapPendingRPCClient(from, to, pendingAdmissionClaim); err != nil {
				t.Fatal(err)
			}
			if resolved, remapped := fixture.app.pendingSessions.Resolve(from); !remapped || resolved != to || fixture.app.rpcClients.Client(to) != fixture.a {
				t.Fatal("remap did not move the pending client after navigation released")
			}
			navigation := treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), from))
			treeNavigationAssertSuccess(t, navigation, to, "branch draft")
		})
	}
}

func TestNavigationRejectsPendingRemapPreparation(t *testing.T) {
	fixture, pending := newPendingAdmissionFixture(t)
	client := &treeNavigationClient{idleRetirementClient: fixture.old, name: "pending", record: func(string) {}}
	client.state = treeNavigationState(false)
	if err := fixture.app.rpcClients.Register(pending, client); err != nil {
		t.Fatal(err)
	}
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
	response := treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), fixture.path))
	treeNavigationAssertConflict(t, response)
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload["retryable"] == false {
		t.Errorf("remap-in-progress conflict must retain ordinary retry behavior: %s", response.Body.String())
	}
	if client.getStateCalls.Load() != 0 {
		t.Error("navigation reached native preflight during remap preparation")
	}
	release()
	idleRetirementWait(t, done, "remap completion")
	if moveErr != nil {
		t.Fatal(moveErr)
	}
	for _, path := range []string{pending, fixture.path} {
		navigation := treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), path))
		treeNavigationAssertSuccess(t, navigation, fixture.path, "branch draft")
	}
	final := fixture.session(t, "final")
	if err := fixture.app.remapPendingRPCClient(fixture.path, final, pendingAdmissionClaim); err != nil {
		t.Fatal(err)
	}
	navigation := treeNavigationServe(t, fixture.handler, treeNavigationRequest(context.Background(), pending))
	treeNavigationAssertSuccess(t, navigation, final, "branch draft")
}
