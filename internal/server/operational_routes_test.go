package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/melounvitek/gripi/internal/config"
	"github.com/melounvitek/gripi/internal/resource"
	"github.com/melounvitek/gripi/internal/update"
)

type fakeResourceMonitor struct {
	snapshot *resource.Snapshot
	err      error
	calls    int
}

func (fake *fakeResourceMonitor) Snapshot() (*resource.Snapshot, error) {
	fake.calls++
	return fake.snapshot, fake.err
}

type fakeUpdateCoordinator struct {
	snapshot                update.Snapshot
	statusCalls, startCalls int
}

func (fake *fakeUpdateCoordinator) CachedStatus() update.Snapshot { return fake.snapshot }
func (fake *fakeUpdateCoordinator) Status() update.Snapshot       { fake.statusCalls++; return fake.snapshot }
func (fake *fakeUpdateCoordinator) Start() update.Snapshot        { fake.startCalls++; return fake.snapshot }

func TestResourceUsagePreservesFrontendJSONContract(t *testing.T) {
	monitor := &fakeResourceMonitor{snapshot: &resource.Snapshot{MemoryBytes: 10, WorkingSetBytes: 8, InactiveFileBytes: 2, CPUUsageUsec: 3, GatewayRSSBytes: 4, PiRSSBytes: 5, PiProcessCount: 2}}
	app := &application{config: config.Config{ResourceMonitoringEnabled: true}, resourceMonitor: monitor}
	response := httptest.NewRecorder()
	app.resourceUsage(response, httptest.NewRequest(http.MethodGet, "/resource-usage", nil))
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"supported", "memoryBytes", "workingSetBytes", "inactiveFileBytes", "cpuUsageUsec", "gatewayRssBytes", "piRssBytes", "piProcessCount"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing %s in %v", key, payload)
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache = %q", response.Header().Get("Cache-Control"))
	}
}
func TestResourceUsageDisabledAndUnsupported(t *testing.T) {
	monitor := &fakeResourceMonitor{}
	app := &application{resourceMonitor: monitor}
	response := httptest.NewRecorder()
	app.resourceUsage(response, httptest.NewRequest(http.MethodGet, "/resource-usage", nil))
	if response.Code != http.StatusNotFound || monitor.calls != 0 {
		t.Fatalf("disabled = %d, calls = %d", response.Code, monitor.calls)
	}
	app.config.ResourceMonitoringEnabled = true
	response = httptest.NewRecorder()
	app.resourceUsage(response, httptest.NewRequest(http.MethodGet, "/resource-usage", nil))
	if response.Body.String() != "{\"supported\":false}" {
		t.Fatalf("unsupported = %s", response.Body.String())
	}
}
func TestGatewayUpdateRoutesExposeInstanceAndCoordinatorState(t *testing.T) {
	message := "2 updates available"
	target := "target"
	behind := 2
	coordinator := &fakeUpdateCoordinator{snapshot: update.Snapshot{State: "available", Message: &message, TargetSHA: &target, BehindCount: &behind}}
	app := &application{instanceID: "instance", updateCoordinator: coordinator}
	response := httptest.NewRecorder()
	app.gatewayUpdateCheck(response, httptest.NewRequest(http.MethodPost, "/gateway-update/check", nil))
	var payload map[string]any
	json.Unmarshal(response.Body.Bytes(), &payload)
	if payload["instanceId"] != "instance" || payload["state"] != "available" || coordinator.statusCalls != 1 {
		t.Fatalf("payload = %v, calls = %d", payload, coordinator.statusCalls)
	}
	response = httptest.NewRecorder()
	app.gatewayUpdateStart(response, httptest.NewRequest(http.MethodPost, "/gateway-update", nil))
	if response.Code != http.StatusAccepted || coordinator.startCalls != 1 {
		t.Fatalf("start = %d, calls = %d", response.Code, coordinator.startCalls)
	}
}
