package server

import (
	"io/fs"
	"net/http"
	"path/filepath"

	"github.com/melounvitek/gripi/internal/agent"
	agentpi "github.com/melounvitek/gripi/internal/agent/pi"
	"github.com/melounvitek/gripi/internal/config"
)

// NewPiHandler builds the gateway with the Pi adapter configured from cfg, as cmd/gripi does.
func NewPiHandler(cfg config.Config, files fs.FS) (http.Handler, error) {
	return newPiHandler(cfg, files, randomBrowserToken)
}

func newPiHandler(cfg config.Config, files fs.FS, newBrowserToken func() (string, error)) (http.Handler, error) {
	extension, err := fs.ReadFile(files, "pi_extensions/gripi-tree.ts")
	if err != nil {
		return nil, err
	}
	stateDir := ""
	if cfg.ReadStatePath != "" {
		stateDir = filepath.Dir(cfg.ReadStatePath)
	}
	adapter := agentpi.New(agentpi.Options{
		Command: cfg.PiCommand, AgentDir: cfg.PiAgentDir, AutoApproveProjects: cfg.AutoApproveProjects, Extension: extension,
		SessionsRoot: cfg.SessionsRoot, Home: cfg.Home, StateDir: stateDir,
	})
	return newHandler(cfg, files, adapter, newBrowserToken)
}

// NewPiStore returns the Pi adapter's store for the sessions under sessionsRoot.
func NewPiStore(sessionsRoot, home string) agent.Store {
	return agentpi.New(agentpi.Options{SessionsRoot: sessionsRoot, Home: home}).Store()
}
