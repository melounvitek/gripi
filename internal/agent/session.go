package agent

import (
	"strings"
	"time"
)

type Session struct {
	Path                           string
	CWD                            string
	ID                             string
	DisplayName                    string
	FirstUserMessage               string
	MessageCount                   int
	AssistantResponseCount         int
	LatestAssistantResponsePreview string
	ParentSessionPath              string
	CreatedAt                      time.Time
	ModifiedAt                     time.Time
	ConversationActivityAt         time.Time
	// FolderMissing is checked on every read rather than cached, so a folder that comes back is noticed.
	FolderMissing bool `json:"-"`
}

type Image struct {
	MIMEType string
	Src      string
	// EntryID and Index locate an image stored inline in the session file.
	EntryID string
	Index   int
}

type Message struct {
	Key                     [2]int
	Role                    string
	Text                    string
	Timestamp               time.Time
	Compact                 bool
	Summary                 string
	Error                   bool
	ToolCallID              string
	ToolName                string
	ToolResultPersisted     bool
	ToolPending             bool
	Thinking                bool
	ToolSummaryHTML         string
	ToolTranscript          bool
	ToolPreview             bool
	ToolPrompt              string
	FinalAssistantResponse  bool
	EntryID                 string
	Images                  []Image
	CustomType              string
	Compaction              bool
	BashExitCode            *int
	BashCancelled           bool
	BashTruncated           bool
	BashExcludedFromContext bool
	BashFullOutputPath      string
	BashRecordedAt          time.Time
	// Omitted marks a placeholder for a message too large to read.
	Omitted bool
}

type Status struct {
	Provider         string
	ModelID          string
	ThinkingLevel    string
	ContextTokens    float64
	ContextLimit     float64
	ContextPercent   float64
	HasContextTokens bool
	HasContextLimit  bool
	ContextEstimated bool
	CostTotal        float64
}

type Window struct {
	Messages            []*Message
	StartIndex          int
	EndIndex            int
	TotalMessageCount   int
	TreeLeafID          string
	LatestStableLeafID  string
	CurrentStableLeafID string
	Status              Status
}

type ToolCallContext struct {
	Prompt          string
	Timestamp       time.Time
	ResultPersisted bool
}

func DisplayHomePath(value, home string) string {
	if home == "" {
		return value
	}
	var result strings.Builder
	for {
		position := strings.Index(value, home)
		if position < 0 {
			result.WriteString(value)
			return result.String()
		}
		end := position + len(home)
		beforeOK := position == 0 || !homePathWordByte(value[position-1])
		afterOK := end == len(value) || value[end] == '/' || !homePathWordByte(value[end])
		if beforeOK && afterOK {
			result.WriteString(value[:position])
			result.WriteByte('~')
			value = value[end:]
		} else {
			result.WriteString(value[:position+1])
			value = value[position+1:]
		}
	}
}

func homePathWordByte(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z' || value >= '0' && value <= '9' || strings.ContainsRune("_.~/-", rune(value))
}

func NotificationPreview(value string) string {
	value = strings.ReplaceAll(strings.ReplaceAll(value, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(strings.ReplaceAll(value, "javascript:", ""), "\n")

	for index := range lines {
		lines[index] = strings.Join(strings.Fields(lines[index]), " ")
	}

	value = strings.TrimSpace(strings.Join(lines, "\n"))

	if len([]rune(value)) > 180 {
		chars := []rune(value)

		return string(chars[:177]) + "…"
	}

	return value
}
