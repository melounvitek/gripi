package rpc

import "github.com/melounvitek/gripi/internal/agent"

// Temporary aliases for the machinery that moved to internal/agent, until callers import it directly.
type (
	RPCClient              = agent.Client
	PromptImage            = agent.PromptImage
	Registry               = agent.Registry
	PendingSession         = agent.PendingSession
	PendingSessionRegistry = agent.PendingSessionRegistry
	Maintenance            = agent.Maintenance
	Diagnostics            = agent.Diagnostics
	RequestTimeoutError    = agent.RequestTimeoutError
	StopResult             = agent.StopResult
)

var (
	NewRegistry               = agent.NewRegistry
	NewPendingSessionRegistry = agent.NewPendingSessionRegistry
	NewMaintenance            = agent.NewMaintenance
	LogMaintenanceError       = agent.LogMaintenanceError
	StopResultFor             = agent.StopResultFor
)

var (
	ErrOperationPending = agent.ErrOperationPending
	ErrInterruptPending = agent.ErrInterruptPending
	ErrBashPending      = agent.ErrBashPending
	ErrClientRetiring   = agent.ErrClientRetiring
	ErrClientStarting   = agent.ErrClientStarting
	ErrProcessExited    = agent.ErrProcessExited
	ErrStartFailed      = agent.ErrStartFailed
)
