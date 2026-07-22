package server

import (
	"net/http"

	"github.com/melounvitek/gripi/internal/resource"
	"github.com/melounvitek/gripi/internal/update"
)

type resourceMonitor interface {
	Snapshot() (*resource.Snapshot, error)
}

type updateCoordinator interface {
	CachedStatus() update.Snapshot
	Status() update.Snapshot
	Start() update.Snapshot
}

func (app *application) registerOperationalRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /resource-usage", app.resourceUsage)
	mux.HandleFunc("GET /gateway-update", app.gatewayUpdateStatus)
	mux.HandleFunc("POST /gateway-update/check", app.gatewayUpdateCheck)
	mux.HandleFunc("POST /gateway-update", app.gatewayUpdateStart)
}

func (app *application) resourceUsage(response http.ResponseWriter, request *http.Request) {
	if !app.config.ResourceMonitoringEnabled {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	snapshot, err := app.resourceMonitor.Snapshot()
	if err != nil || snapshot == nil {
		writeJSON(response, map[string]bool{"supported": false})
		return
	}
	writeJSON(response, map[string]any{
		"supported":         true,
		"memoryBytes":       snapshot.MemoryBytes,
		"workingSetBytes":   snapshot.WorkingSetBytes,
		"inactiveFileBytes": snapshot.InactiveFileBytes,
		"cpuUsageUsec":      snapshot.CPUUsageUsec,
		"gatewayRssBytes":   snapshot.GatewayRSSBytes,
		"piRssBytes":        snapshot.PiRSSBytes,
		"piProcessCount":    snapshot.PiProcessCount,
	})
}

func (app *application) gatewayUpdateStatus(response http.ResponseWriter, _ *http.Request) {
	app.writeUpdateSnapshot(response, http.StatusOK, app.updateCoordinator.CachedStatus())
}

func (app *application) gatewayUpdateCheck(response http.ResponseWriter, _ *http.Request) {
	app.writeUpdateSnapshot(response, http.StatusOK, app.updateCoordinator.Status())
}

func (app *application) gatewayUpdateStart(response http.ResponseWriter, _ *http.Request) {
	app.writeUpdateSnapshot(response, http.StatusAccepted, app.updateCoordinator.Start())
}

func (app *application) writeUpdateSnapshot(response http.ResponseWriter, status int, snapshot update.Snapshot) {
	response.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(response, status, struct {
		InstanceID         string  `json:"instanceId"`
		State              string  `json:"state"`
		Reason             *string `json:"reason"`
		Message            *string `json:"message"`
		CurrentSHA         *string `json:"currentSha"`
		TargetSHA          *string `json:"targetSha"`
		BehindCount        *int    `json:"behindCount"`
		Summary            *string `json:"summary"`
		ActiveSessionCount *int    `json:"activeSessionCount"`
	}{
		InstanceID:         app.instanceID,
		State:              snapshot.State,
		Reason:             snapshot.Reason,
		Message:            snapshot.Message,
		CurrentSHA:         snapshot.CurrentSHA,
		TargetSHA:          snapshot.TargetSHA,
		BehindCount:        snapshot.BehindCount,
		Summary:            snapshot.Summary,
		ActiveSessionCount: snapshot.ActiveSessionCount,
	})
}
