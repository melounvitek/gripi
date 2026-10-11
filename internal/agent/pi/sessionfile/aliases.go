package sessionfile

import "github.com/melounvitek/gripi/internal/agent"

// Temporary aliases while callers move to the agent package.
type (
	Session         = agent.Session
	Image           = agent.Image
	Message         = agent.Message
	Status          = agent.Status
	Window          = agent.Window
	ToolCallContext = agent.ToolCallContext

	SyncMode         = agent.SyncMode
	SyncResult       = agent.SyncResult
	SyncBlockedError = agent.SyncBlockedError
)

const (
	SyncAvailable      = agent.SyncAvailable
	SyncManaged        = agent.SyncManaged
	SyncExternalFollow = agent.SyncExternalFollow
	SyncConflict       = agent.SyncConflict
	SyncFolderMissing  = agent.SyncFolderMissing
)

var ErrSyncBusy = agent.ErrSyncBusy

func DisplayHomePath(value, home string) string { return agent.DisplayHomePath(value, home) }

func NotificationPreview(value string) string { return agent.NotificationPreview(value) }
