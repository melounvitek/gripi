package server

import (
	"io/fs"
	"net/http"

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
	adapter := agentpi.New(agentpi.Options{Command: cfg.PiCommand, AgentDir: cfg.PiAgentDir, AutoApproveProjects: cfg.AutoApproveProjects, Extension: extension})
	return newHandler(cfg, files, adapter, newBrowserToken)
}
