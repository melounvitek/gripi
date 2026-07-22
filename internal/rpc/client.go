package rpc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	DefaultEventBufferLimit                  = 5_000
	DefaultEventBufferBytes                  = 8 << 20
	RPCReadChunkBytes                        = 64 << 10
	MaxSampledToolUpdateBytes                = 1 << 20
	MaxFallbackRPCLineBytes                  = 65 << 20
	DefaultOversizedToolUpdateSampleInterval = 2 * time.Second
	MaxOversizedToolUpdateSampleKeys         = 64
	DefaultRequestTimeout                    = 30 * time.Second
	LongRequestTimeout                       = 5 * time.Minute
	AbortRequestTimeout                      = 10 * time.Second
	ProcessTermTimeout                       = time.Second
	ProcessKillTimeout                       = time.Second
	TreeBridgeTimeout                        = 5 * time.Second
	MaxActiveToolSnapshots                   = 16
	MaxCompletedBashEvents                   = 16
	MaxActiveToolSnapshotBytes               = 64 << 10
	MaxActiveToolSnapshotIDBytes             = 1_024
	MaxQueuedMessageCount                    = 64
	MaxQueuedMessageBytes                    = 64 << 10
	MaxExtensionUIItems                      = 32
	MaxExtensionUIItemBytes                  = 16 << 10
	MaxExtensionUISnapshotBytes              = 256 << 10
	MaxSnapshotStringBytes                   = 8 << 10
	MaxCompactionFollowUps                   = 64
	MaxCompactionFollowUpBytes               = 64 << 20
	activeToolSnapshotToolLimit              = 10
	activeToolSnapshotOutputBytes            = 1_024
	activeToolSnapshotTextBytes              = 4 << 10
	snapshotToolName                         = "subagent"
)

var nativeToolUpdatePrefix = regexp.MustCompile(`^\{"type":"tool_execution_update","toolCallId":"((?:\\.|[^"\\])*)"`)

var (
	ErrProcessExited      = errors.New("Pi RPC process exited")
	ErrRPCLineTooLarge    = fmt.Errorf("%w: RPC response exceeds the compatibility limit", ErrProcessExited)
	ErrBashAlreadyRunning = errors.New("a bash command is already running for this Pi RPC client")
)

type RequestTimeoutError struct{ Command string }

func (err *RequestTimeoutError) Error() string { return "Pi RPC command timed out: " + err.Command }

type BashRequestError struct {
	BashID string
	Err    error
}

func (err *BashRequestError) Error() string { return err.Err.Error() }
func (err *BashRequestError) Unwrap() error { return err.Err }

type EventBatch struct {
	Events  []map[string]any `json:"events"`
	LastSeq int64            `json:"last_seq"`
	Missed  bool             `json:"missed"`
}

type LiveSnapshot struct {
	EventSequence       int64               `json:"event_sequence"`
	EventReplayCursor   int64               `json:"event_replay_cursor"`
	ActiveToolEvents    []map[string]any    `json:"active_tool_events"`
	Busy                bool                `json:"busy,omitempty"`
	BusySince           *time.Time          `json:"busy_since,omitempty"`
	AgentBusySince      *time.Time          `json:"agent_busy_since,omitempty"`
	AgentRunning        bool                `json:"agent_running,omitempty"`
	ActiveBash          map[string]any      `json:"active_bash,omitempty"`
	CompletedBashEvents []map[string]any    `json:"completed_bash_events,omitempty"`
	Compacting          bool                `json:"compacting,omitempty"`
	CompactingSince     *time.Time          `json:"compacting_since,omitempty"`
	QueuedMessages      map[string][]string `json:"queued_messages,omitempty"`
	ExtensionUI         map[string]any      `json:"extension_ui,omitempty"`
}

type SessionEntries struct {
	Known   bool
	LeafID  string
	Entries []map[string]any
	Error   string
}

type ClientOptions struct {
	EventBufferLimit                  int
	EventBufferBytes                  int
	RequestTimeout                    time.Duration
	AbortTimeout                      time.Duration
	TreeBridgeTimeout                 time.Duration
	OversizedToolUpdateSampleInterval time.Duration
	ProcessTermTimeout                time.Duration
	ProcessKillTimeout                time.Duration
	FallbackRPCLineBytes              int
	Clock                             func() time.Time
	Now                               func() time.Time
	Diagnostics                       *Diagnostics
}

type responseResult struct {
	value map[string]any
}

type replayEntry struct {
	sequence int64
	event    map[string]any
	bytes    int
	key      string
}

type extensionDialog struct {
	event     map[string]any
	expiresAt time.Time
	answering bool
}

type bashToken struct {
	id                 string
	command            string
	excludeFromContext bool
	started            bool
	startedAt          time.Time
}

type Client struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr io.ReadCloser

	process            *exec.Cmd
	processGroup       processGroup
	pid                int
	waitDone           chan struct{}
	processTermTimeout time.Duration
	processKillTimeout time.Duration

	requestTimeout       time.Duration
	abortTimeout         time.Duration
	fallbackRPCLineBytes int
	treeBridgeTimeout    time.Duration
	clock                func() time.Time
	now                  func() time.Time
	diagnostics          *Diagnostics

	writeLane  chan struct{}
	closeMu    sync.Mutex
	closed     bool
	readerDone chan struct{}
	stderrDone chan struct{}
	readerErr  error

	mu                 sync.Mutex
	requestSequence    int64
	pending            map[string]chan responseResult
	bridgePending      map[string]chan string
	deferredCommandIDs map[string]bool

	events           []replayEntry
	eventBufferSize  int
	eventBufferLimit int
	eventBufferBytes int
	eventReplayFloor int64
	eventSequence    int64
	coalesced        map[string]*replayEntry

	activeToolEvents     map[string]map[string]any
	queuedMessages       map[string][]string
	pendingDialogs       map[string]*extensionDialog
	pendingDialogOrder   []string
	extensionStatuses    map[string]map[string]any
	extensionStatusOrder []string
	extensionWidgets     map[string]map[string]any
	extensionWidgetOrder []string
	extensionTitle       map[string]any

	busy                        bool
	busySince                   *time.Time
	agentRunning                bool
	settledAt                   *time.Time
	activeBashToken             *bashToken
	activeBashID                string
	activeBashCommand           string
	activeBashExclude           bool
	activeBashStartedAt         *time.Time
	completedBashEvents         map[string]map[string]any
	completedBashOrder          []string
	compacting                  bool
	compactingSince             *time.Time
	compactionFollowUps         []map[string]any
	compactionFollowUpCount     int
	compactionFollowUpBytes     int
	flushingCompactionFollowUps bool

	sampledToolUpdates map[string]time.Time
	sampleInterval     time.Duration
}

func Start(sessionPath string, command []string, extensionPath string, diagnostics *Diagnostics) (*Client, error) {
	args := []string{"--mode", "rpc", "--extension", extensionPath, "--session", sessionPath}
	return startProcess("", command, args, diagnostics)
}

func StartInCWD(cwd string, command []string, extensionPath string, diagnostics *Diagnostics) (*Client, error) {
	args := []string{"--mode", "rpc", "--extension", extensionPath}
	return startProcess(cwd, command, args, diagnostics)
}

func startProcess(cwd string, command, args []string, diagnostics *Diagnostics) (*Client, error) {
	if len(command) == 0 {
		return nil, errors.New("Pi RPC command is empty")
	}
	cmd := exec.Command(command[0], append(command[1:], args...)...)
	cmd.Dir = cwd
	cmd.Env = ScrubbedEnvironment(os.Environ())
	configureProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		return nil, err
	}
	group, err := attachProcessGroup(cmd)
	if err != nil {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = stdout.Close()
		_ = stderr.Close()
		_ = cmd.Wait()
		return nil, fmt.Errorf("attach Pi RPC process group: %w", err)
	}
	client := NewClient(stdin, stdout, stderr, ClientOptions{Diagnostics: diagnostics})
	client.process = cmd
	client.processGroup = group
	client.pid = cmd.Process.Pid
	client.waitDone = make(chan struct{})
	go client.waitForProcess()
	return client, nil
}

func ScrubbedEnvironment(environment []string) []string {
	blocked := map[string]bool{"GEM_HOME": true, "GEM_PATH": true, "RUBYLIB": true, "RUBYOPT": true, "GRIPI_ADMIN_PASSWORD": true}
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		if blocked[key] || strings.HasPrefix(key, "BUNDLE_") || strings.HasPrefix(key, "BUNDLER_") {
			continue
		}
		result = append(result, entry)
	}
	return result
}

func NewClient(stdin io.WriteCloser, stdout io.ReadCloser, stderr io.ReadCloser, options ClientOptions) *Client {
	if options.EventBufferLimit == 0 {
		options.EventBufferLimit = DefaultEventBufferLimit
	}
	if options.EventBufferBytes == 0 {
		options.EventBufferBytes = DefaultEventBufferBytes
	}
	if options.RequestTimeout == 0 {
		options.RequestTimeout = DefaultRequestTimeout
	}
	if options.AbortTimeout == 0 {
		options.AbortTimeout = AbortRequestTimeout
	}
	if options.TreeBridgeTimeout == 0 {
		options.TreeBridgeTimeout = TreeBridgeTimeout
	}
	if options.OversizedToolUpdateSampleInterval == 0 {
		options.OversizedToolUpdateSampleInterval = DefaultOversizedToolUpdateSampleInterval
	}
	if options.ProcessTermTimeout == 0 {
		options.ProcessTermTimeout = ProcessTermTimeout
	}
	if options.ProcessKillTimeout == 0 {
		options.ProcessKillTimeout = ProcessKillTimeout
	}
	if options.FallbackRPCLineBytes == 0 {
		options.FallbackRPCLineBytes = MaxFallbackRPCLineBytes
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	client := &Client{
		stdin: stdin, stdout: stdout, stderr: stderr,
		processTermTimeout: options.ProcessTermTimeout, processKillTimeout: options.ProcessKillTimeout,
		requestTimeout: options.RequestTimeout, abortTimeout: options.AbortTimeout, fallbackRPCLineBytes: options.FallbackRPCLineBytes, treeBridgeTimeout: options.TreeBridgeTimeout,
		clock: options.Clock, now: options.Now, diagnostics: options.Diagnostics,
		writeLane: make(chan struct{}, 1), readerDone: make(chan struct{}), stderrDone: make(chan struct{}),
		pending: make(map[string]chan responseResult), bridgePending: make(map[string]chan string), deferredCommandIDs: make(map[string]bool),
		eventBufferLimit: options.EventBufferLimit, eventBufferBytes: options.EventBufferBytes, coalesced: make(map[string]*replayEntry),
		activeToolEvents: make(map[string]map[string]any), queuedMessages: map[string][]string{"steering": {}, "followUp": {}},
		pendingDialogs: make(map[string]*extensionDialog), extensionStatuses: make(map[string]map[string]any), extensionWidgets: make(map[string]map[string]any),
		completedBashEvents: make(map[string]map[string]any), sampledToolUpdates: make(map[string]time.Time), sampleInterval: options.OversizedToolUpdateSampleInterval,
	}
	client.writeLane <- struct{}{}
	go client.readStdout()
	if stderr != nil {
		go client.readStderr()
	} else {
		close(client.stderrDone)
	}
	return client
}

func (client *Client) nextID(command string) string {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.requestSequence++
	return fmt.Sprintf("%s-%d", command, client.requestSequence)
}

func (client *Client) GetState(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "get_state", client.nextID("get_state"), nil, client.requestTimeout, nil)
}
func (client *Client) GetStateForInterrupt(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "get_state", client.nextID("get_state"), nil, client.abortTimeout, nil)
}
func (client *Client) GetMessages(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "get_messages", client.nextID("get_messages"), nil, client.requestTimeout, nil)
}
func (client *Client) GetSessionStats(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "get_session_stats", client.nextID("get_session_stats"), nil, client.requestTimeout, nil)
}
func (client *Client) GetAvailableModels(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "get_available_models", client.nextID("get_available_models"), nil, client.requestTimeout, nil)
}
func (client *Client) GetCommands(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "get_commands", client.nextID("get_commands"), nil, client.requestTimeout, nil)
}

func (client *Client) SessionEntriesAfter(ctx context.Context, cursor string) (SessionEntries, error) {
	payload := map[string]any{}
	if cursor != "" {
		payload["since"] = cursor
	}
	response, err := client.request(ctx, "get_entries", client.nextID("get_entries"), payload, client.requestTimeout, nil)
	if err != nil {
		return SessionEntries{}, err
	}
	if response["success"] == true {
		if data, ok := response["data"].(map[string]any); ok {
			if raw, ok := data["entries"].([]any); ok {
				entries := make([]map[string]any, 0, len(raw))
				for _, item := range raw {
					entry, ok := item.(map[string]any)
					if !ok {
						return SessionEntries{Error: "Pi RPC get_entries returned an invalid entry"}, nil
					}
					entries = append(entries, entry)
				}
				leafID := ""
				if data["leafId"] != nil {
					var ok bool
					leafID, ok = data["leafId"].(string)
					if !ok {
						return SessionEntries{Error: "Pi RPC get_entries returned an invalid leafId"}, nil
					}
				}
				return SessionEntries{Known: true, LeafID: leafID, Entries: entries}, nil
			}
		}
	}
	message := stringValue(response["error"])
	if strings.HasPrefix(message, "Entry not found:") {
		return SessionEntries{}, nil
	}
	if message == "" {
		message = "Pi RPC get_entries failed"
	}
	return SessionEntries{Error: message}, nil
}

func (client *Client) SessionPosition(ctx context.Context, cursor string) (SessionEntries, error) {
	result, err := client.SessionEntriesAfter(ctx, cursor)
	result.Entries = nil
	return result, err
}

func (client *Client) SetModel(ctx context.Context, provider, modelID string) (map[string]any, error) {
	return client.request(ctx, "set_model", client.nextID("set_model"), map[string]any{"provider": provider, "modelId": modelID}, client.requestTimeout, nil)
}
func (client *Client) SetThinkingLevel(ctx context.Context, level string) (map[string]any, error) {
	return client.request(ctx, "set_thinking_level", client.nextID("set_thinking_level"), map[string]any{"level": level}, client.requestTimeout, nil)
}
func (client *Client) CycleThinkingLevel(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "cycle_thinking_level", client.nextID("cycle_thinking_level"), nil, client.requestTimeout, nil)
}
func (client *Client) Prompt(ctx context.Context, message string, images []PromptImage) (map[string]any, error) {
	deadline := client.now().Add(client.requestTimeout)
	if err := client.waitForCompactionFlush(ctx, deadline); err != nil {
		return nil, err
	}
	payload := map[string]any{"message": message}
	if len(images) > 0 {
		payload["images"] = images
	}
	remaining := deadline.Sub(client.now())
	if remaining <= 0 {
		return nil, &RequestTimeoutError{Command: "prompt"}
	}
	return client.request(ctx, "prompt", client.nextID("prompt"), payload, remaining, nil)
}
func (client *Client) Steer(ctx context.Context, message string, images []PromptImage) (map[string]any, error) {
	payload := map[string]any{"message": message}
	if len(images) > 0 {
		payload["images"] = images
	}
	return client.request(ctx, "steer", client.nextID("steer"), payload, client.requestTimeout, nil)
}
func (client *Client) FollowUp(ctx context.Context, message string, images []PromptImage) (map[string]any, error) {
	if response, queued, err := client.QueueCompactionFollowUp(ctx, message, images); err != nil || queued {
		return response, err
	}
	return client.request(ctx, "follow_up", client.nextID("follow_up"), promptPayload(message, images), client.requestTimeout, nil)
}

func (client *Client) QueueCompactionFollowUp(_ context.Context, message string, images []PromptImage) (map[string]any, bool, error) {
	payload := promptPayload(message, images)
	size := promptPayloadSize(payload)
	client.mu.Lock()
	defer client.mu.Unlock()
	if !client.compacting && !client.flushingCompactionFollowUps {
		return nil, false, nil
	}
	if client.compactionFollowUpCount >= MaxCompactionFollowUps || client.compactionFollowUpBytes+size > MaxCompactionFollowUpBytes {
		return map[string]any{"type": "response", "command": "follow_up", "success": false, "error": "Too many follow-up messages are waiting for compaction to finish"}, true, nil
	}
	client.compactionFollowUps = append(client.compactionFollowUps, payload)
	client.compactionFollowUpCount++
	client.compactionFollowUpBytes += size
	return map[string]any{"type": "response", "command": "follow_up", "success": true, "queued": true, "compacting": true}, true, nil
}

func promptPayload(message string, images []PromptImage) map[string]any {
	payload := map[string]any{"message": message}
	if len(images) > 0 {
		payload["images"] = images
	}
	return payload
}
func (client *Client) Abort(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "abort", client.nextID("abort"), nil, client.abortTimeout, nil)
}
func (client *Client) AbortBash(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "abort_bash", client.nextID("abort_bash"), nil, client.abortTimeout, nil)
}
func (client *Client) NewSession(ctx context.Context, parent string) (map[string]any, error) {
	payload := map[string]any{}
	if parent != "" {
		payload["parentSession"] = parent
	}
	return client.request(ctx, "new_session", client.nextID("new_session"), payload, LongRequestTimeout, nil)
}
func (client *Client) SwitchSession(ctx context.Context, path string) (map[string]any, error) {
	return client.request(ctx, "switch_session", client.nextID("switch_session"), map[string]any{"sessionPath": path}, LongRequestTimeout, nil)
}
func (client *Client) Compact(ctx context.Context, instructions string) (map[string]any, error) {
	payload := map[string]any{}
	if instructions != "" {
		payload["customInstructions"] = instructions
	}
	return client.request(ctx, "compact", client.nextID("compact"), payload, LongRequestTimeout, nil)
}
func (client *Client) GetForkMessages(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "get_fork_messages", client.nextID("get_fork_messages"), nil, client.requestTimeout, nil)
}
func (client *Client) Fork(ctx context.Context, entryID string) (map[string]any, error) {
	return client.request(ctx, "fork", client.nextID("fork"), map[string]any{"entryId": entryID}, LongRequestTimeout, nil)
}
func (client *Client) CloneSession(ctx context.Context) (map[string]any, error) {
	return client.request(ctx, "clone", client.nextID("clone"), nil, LongRequestTimeout, nil)
}
func (client *Client) SetSessionName(ctx context.Context, name string) (map[string]any, error) {
	return client.request(ctx, "set_session_name", client.nextID("set_session_name"), map[string]any{"name": name}, client.requestTimeout, nil)
}

func (client *Client) request(ctx context.Context, command, id string, payload map[string]any, timeout time.Duration, accepted func()) (map[string]any, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	value := make(map[string]any, len(payload)+2)
	for key, item := range payload {
		value[key] = item
	}
	value["id"], value["type"] = id, command
	client.diagnostics.Log("command_started", map[string]any{"command": command, "rpc_id": id})
	pending := make(chan responseResult, 1)
	client.mu.Lock()
	client.pending[id] = pending
	client.mu.Unlock()
	deadline := time.Time{}
	if timeout > 0 {
		deadline = client.now().Add(timeout)
	}
	if err := client.writeCommand(ctx, value, command, deadline); err != nil {
		client.removePending(id)
		if isTimeout(err) {
			client.diagnostics.Log("command_timed_out", map[string]any{"command": command, "rpc_id": id, "stage": "write"})
		}
		return nil, err
	}
	if accepted != nil {
		accepted()
	}

	var timer <-chan time.Time
	var timeoutTimer *time.Timer
	if !deadline.IsZero() {
		remaining := deadline.Sub(client.now())
		if remaining < 0 {
			remaining = 0
		}
		timeoutTimer = time.NewTimer(remaining)
		timer = timeoutTimer.C
		defer timeoutTimer.Stop()
	}
	select {
	case result := <-pending:
		client.diagnostics.Log("response_received", map[string]any{"command": command, "rpc_id": id})
		return result.value, nil
	case <-timer:
		client.removePending(id)
		client.diagnostics.Log("command_timed_out", map[string]any{"command": command, "rpc_id": id, "stage": "response"})
		return nil, &RequestTimeoutError{Command: command}
	case <-client.readerDone:
		select {
		case result := <-pending:
			client.diagnostics.Log("response_received", map[string]any{"command": command, "rpc_id": id})
			return result.value, nil
		default:
			client.removePending(id)
			return nil, client.readerFailure("before responding to command")
		}
	}
}

func (client *Client) removePending(id string) {
	client.mu.Lock()
	delete(client.pending, id)
	client.mu.Unlock()
}

func (client *Client) readerFailure(stage string) error {
	client.mu.Lock()
	err := client.readerErr
	client.mu.Unlock()
	if err != nil {
		return err
	}
	return fmt.Errorf("%w %s", ErrProcessExited, stage)
}

func (client *Client) writeCommand(ctx context.Context, value map[string]any, command string, deadline time.Time) error {
	var timer <-chan time.Time
	var lockTimer *time.Timer
	if !deadline.IsZero() {
		remaining := deadline.Sub(client.now())
		if remaining < 0 {
			remaining = 0
		}
		lockTimer = time.NewTimer(remaining)
		timer = lockTimer.C
		defer lockTimer.Stop()
	}
	select {
	case <-client.writeLane:
		defer func() { client.writeLane <- struct{}{} }()
	case <-timer:
		return &RequestTimeoutError{Command: command}
	case <-ctx.Done():
		return ctx.Err()
	case <-client.readerDone:
		return client.readerFailure("before accepting command")
	}
	return client.writeCommandUnlocked(value, command, deadline)
}

func (client *Client) writeCommandUnlocked(value map[string]any, command string, deadline time.Time) error {
	if !deadline.IsZero() {
		if setter, ok := client.stdin.(interface{ SetWriteDeadline(time.Time) error }); ok {
			remaining := deadline.Sub(client.now())
			if remaining < 0 {
				remaining = 0
			}
			_ = setter.SetWriteDeadline(time.Now().Add(remaining))
			defer setter.SetWriteDeadline(time.Time{})
		}
	}
	writer := &commandWriter{destination: client.stdin, command: command}
	if images, ok := value["images"].([]PromptImage); ok && len(images) > 0 {
		return writeImageCommand(writer, value, images)
	}
	encoded, err := marshalJSON(value)
	if err != nil {
		return err
	}
	_, err = writer.Write(append(encoded, '\n'))
	return err
}

type commandWriter struct {
	destination io.Writer
	command     string
}

func (writer *commandWriter) Write(value []byte) (int, error) {
	written := 0
	for len(value) > 0 {
		count, err := writer.destination.Write(value)
		written += count
		value = value[count:]
		if err != nil {
			if isNetTimeout(err) {
				return written, &RequestTimeoutError{Command: writer.command}
			}
			return written, fmt.Errorf("%w before accepting command: %v", ErrProcessExited, err)
		}
		if count == 0 {
			return written, fmt.Errorf("%w before accepting command: short write", ErrProcessExited)
		}
	}
	return written, nil
}

func writeImageCommand(writer io.Writer, value map[string]any, images []PromptImage) error {
	files := make([]*os.File, 0, len(images))
	for _, image := range images {
		file, err := os.Open(image.Path)
		if err == nil {
			var info os.FileInfo
			info, err = file.Stat()
			if err == nil && (image.Size < 0 || info.Size() != image.Size) {
				err = errors.New("persisted prompt image size changed")
			}
		}
		if err != nil {
			if file != nil {
				_ = file.Close()
			}
			for _, opened := range files {
				_ = opened.Close()
			}
			return err
		}
		files = append(files, file)
	}
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
	}()
	if _, err := io.WriteString(writer, `{`); err != nil {
		return err
	}
	first := true
	for _, key := range []string{"message", "id", "type"} {
		item, exists := value[key]
		if !exists {
			continue
		}
		encoded, err := marshalJSON(item)
		if err != nil {
			return err
		}
		separator := ""
		if !first {
			separator = ","
		}
		if _, err := fmt.Fprintf(writer, `%s%q:`, separator, key); err != nil {
			return err
		}
		if _, err := writer.Write(encoded); err != nil {
			return err
		}
		first = false
	}
	separator := ""
	if !first {
		separator = ","
	}
	if _, err := fmt.Fprintf(writer, `%s"images":[`, separator); err != nil {
		return err
	}
	for index, image := range images {
		if index > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		mimeType, _ := marshalJSON(image.MIMEType)
		if _, err := io.WriteString(writer, `{"type":"image","data":"`); err != nil {
			return err
		}
		encoder := base64.NewEncoder(base64.StdEncoding, writer)
		copied, copyErr := io.Copy(encoder, io.LimitReader(files[index], image.Size+1))
		if copyErr != nil {
			_ = encoder.Close()
			return copyErr
		}
		if copied != image.Size {
			_ = encoder.Close()
			return fmt.Errorf("%w before accepting command: persisted prompt image size changed", ErrProcessExited)
		}
		if err := encoder.Close(); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, `","mimeType":`); err != nil {
			return err
		}
		if _, err := writer.Write(mimeType); err != nil {
			return err
		}
		if _, err := io.WriteString(writer, `}`); err != nil {
			return err
		}
	}
	_, err := io.WriteString(writer, "]}\n")
	return err
}

func (client *Client) readStdout() {
	defer close(client.readerDone)
	defer client.readerStopped()
	err := client.readLines(func(line []byte) {
		if len(bytes.TrimSpace(line)) == 0 {
			return
		}
		var value map[string]any
		if json.Unmarshal(line, &value) != nil {
			return
		}
		client.storeResponse(value, len(line))
	})
	if err != nil {
		client.mu.Lock()
		client.readerErr = err
		client.mu.Unlock()
	}
}

func (client *Client) readStderr() {
	_, _ = io.Copy(io.Discard, client.stderr)
	close(client.stderrDone)
}

func (client *Client) readLines(yield func([]byte)) error {
	chunk := make([]byte, RPCReadChunkBytes)
	line := make([]byte, 0, RPCReadChunkBytes)
	mode := "unclassified"
	for {
		count, err := client.stdout.Read(chunk)
		offset := 0
		for offset < count {
			rest := chunk[offset:count]
			newlineAt := bytes.IndexByte(rest, '\n')
			length := len(rest)
			if newlineAt >= 0 {
				length = newlineAt + 1
			}
			if mode != "discard" {
				line = append(line, rest[:length]...)
				if mode == "unclassified" && len(line) >= RPCReadChunkBytes {
					id, native, trackable := nativeOversizedToolUpdateID(line)
					switch {
					case native && !trackable:
						mode = "discard"
						line = line[:0]
					case native && client.sampleOversizedToolUpdate(id):
						mode = "sampled"
					case native:
						mode = "discard"
						line = line[:0]
					default:
						mode = "fallback"
					}
				} else if mode == "sampled" && len(line) > MaxSampledToolUpdateBytes {
					mode = "discard"
					line = line[:0]
				} else if mode == "fallback" && len(line) > client.fallbackRPCLineBytes {
					return ErrRPCLineTooLarge
				}
			}
			if newlineAt >= 0 {
				if mode != "discard" {
					yield(append([]byte(nil), line...))
				}
				line = line[:0]
				mode = "unclassified"
			}
			offset += length
		}
		if err != nil {
			if len(line) > 0 && mode != "discard" {
				if mode == "fallback" && len(line) > client.fallbackRPCLineBytes {
					return ErrRPCLineTooLarge
				}
				yield(line)
			}
			return nil
		}
	}
}

func nativeOversizedToolUpdateID(fragment []byte) (string, bool, bool) {
	const prefix = `{"type":"tool_execution_update","toolCallId":"`
	if !bytes.HasPrefix(fragment, []byte(prefix)) {
		return "", false, false
	}
	match := nativeToolUpdatePrefix.FindSubmatch(fragment)
	if match == nil {
		return "", true, false
	}
	var id string
	if json.Unmarshal(append(append([]byte{'"'}, match[1]...), '"'), &id) != nil || len(id) > MaxActiveToolSnapshotIDBytes {
		return "", true, false
	}
	return id, true, true
}

func (client *Client) sampleOversizedToolUpdate(id string) bool {
	now := client.now()
	for key, sampled := range client.sampledToolUpdates {
		if now.Sub(sampled) >= client.sampleInterval {
			delete(client.sampledToolUpdates, key)
		}
	}
	if _, exists := client.sampledToolUpdates[id]; exists || len(client.sampledToolUpdates) >= MaxOversizedToolUpdateSampleKeys {
		return false
	}
	client.sampledToolUpdates[id] = now
	return true
}

func (client *Client) readerStopped() {
	client.mu.Lock()
	client.agentRunning = false
	client.activeBashToken = nil
	client.activeBashID = ""
	client.activeBashCommand = ""
	client.activeBashStartedAt = nil
	client.compacting = false
	client.compactingSince = nil
	client.compactionFollowUps = nil
	client.compactionFollowUpCount = 0
	client.compactionFollowUpBytes = 0
	client.flushingCompactionFollowUps = false
	client.deferredCommandIDs = make(map[string]bool)
	client.activeToolEvents = make(map[string]map[string]any)
	client.completedBashEvents = make(map[string]map[string]any)
	client.completedBashOrder = nil
	client.queuedMessages = map[string][]string{"steering": {}, "followUp": {}}
	client.pendingDialogs = make(map[string]*extensionDialog)
	client.pendingDialogOrder = nil
	client.extensionStatuses = make(map[string]map[string]any)
	client.extensionStatusOrder = nil
	client.extensionWidgets = make(map[string]map[string]any)
	client.extensionWidgetOrder = nil
	client.extensionTitle = nil
	client.busy = false
	client.busySince = nil
	client.mu.Unlock()
}

func (client *Client) storeResponse(response map[string]any, serializedBytes int) {
	typeName := stringValue(response["type"])
	if typeName == "agent_end" {
		client.sampledToolUpdates = make(map[string]time.Time)
	}
	if typeName == "tool_execution_end" {
		delete(client.sampledToolUpdates, stringValue(response["toolCallId"]))
	}

	var followUps []map[string]any
	firstType := "prompt"
	client.mu.Lock()
	storeAsEvent := false
	if key := internalBridgeStatusKey(response); key != "" {
		if target := client.bridgePending[key]; target != nil {
			select {
			case target <- stringValue(response["statusText"]):
			default:
			}
		}
	} else if id := stringValue(response["id"]); id != "" && client.pending[id] != nil {
		target := client.pending[id]
		delete(client.pending, id)
		target <- responseResult{value: response}
	} else if id := stringValue(response["id"]); id != "" && client.deferredCommandIDs[id] {
		delete(client.deferredCommandIDs, id)
		storeAsEvent = response["success"] == false
	} else if typeName == "response" && stringValue(response["id"]) != "" {
		// A response whose request timed out is intentionally discarded.
	} else {
		storeAsEvent = true
	}

	if storeAsEvent {
		if typeName == "agent_start" || typeName == "agent_settled" || typeName == "turn_start" || typeName == "compaction_start" {
			response = cloneMap(response)
			response["gatewayTimestamp"] = client.clock().UnixMilli()
			serializedBytes = jsonSize(response)
		}
		client.updateBusyStateLocked(response)
		client.updateQueuedMessagesLocked(response)
		client.updateExtensionUILocked(response)
		if (typeName == "compaction" || typeName == "compaction_end") && !client.flushingCompactionFollowUps && len(client.compactionFollowUps) > 0 {
			followUps = client.compactionFollowUps
			client.compactionFollowUps = nil
			client.flushingCompactionFollowUps = true
			if typeName == "compaction_end" && response["willRetry"] == true {
				firstType = "follow_up"
			}
		}
		client.updateActiveToolsLocked(response, serializedBytes)
		client.discardSupersededLocked(response)
		client.eventSequence++
		replay, size := client.boundedReplayEvent(response, serializedBytes)
		if replay != nil {
			client.appendReplayLocked(replay, size, replayCoalesceKey(response))
		} else {
			client.discardReplayLocked()
		}
	}
	client.mu.Unlock()
	if len(followUps) > 0 {
		go client.flushCompactionFollowUps(followUps, firstType)
	}
}

func (client *Client) EventsAfter(after int64) EventBatch {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.pruneExpiredDialogsLocked()
	result := EventBatch{LastSeq: client.eventSequence, Missed: after < client.eventReplayFloor, Events: []map[string]any{}}
	if result.Missed {
		return result
	}
	for _, entry := range client.events {
		if entry.sequence <= after {
			continue
		}
		if event := client.extensionUIEventForDeliveryLocked(entry.event); event != nil {
			result.Events = append(result.Events, event)
		}
	}
	return result
}

func (client *Client) EventSequence() int64 {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.eventSequence
}
func (client *Client) EventReplayCursor() int64 {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.eventReplayFloor
}
func (client *Client) Busy() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.busy || client.activeBashToken != nil
}
func (client *Client) AgentRunning() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.agentRunning
}
func (client *Client) Compacting() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.compacting
}
func (client *Client) SettledAt() *time.Time {
	client.mu.Lock()
	defer client.mu.Unlock()
	return copyTime(client.settledAt)
}
func (client *Client) BusySince() *time.Time {
	client.mu.Lock()
	defer client.mu.Unlock()
	return copyTime(client.busySinceLocked())
}
func (client *Client) ActiveBashCommand() string {
	client.mu.Lock()
	defer client.mu.Unlock()
	return client.activeBashCommand
}

func (client *Client) LiveSnapshot() LiveSnapshot {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.pruneExpiredDialogsLocked()
	result := LiveSnapshot{EventSequence: client.eventSequence, EventReplayCursor: client.eventReplayFloor, ActiveToolEvents: make([]map[string]any, 0, len(client.activeToolEvents))}
	for _, event := range client.activeToolEvents {
		result.ActiveToolEvents = append(result.ActiveToolEvents, event)
	}
	result.Busy = client.busy || client.activeBashToken != nil
	result.BusySince = copyTime(client.busySinceLocked())
	result.AgentBusySince = copyTime(client.busySince)
	result.AgentRunning = client.agentRunning
	if client.activeBashStartedAt != nil {
		result.ActiveBash = map[string]any{"bash_id": client.activeBashID, "command": client.activeBashCommand, "exclude_from_context": client.activeBashExclude, "started_at": *client.activeBashStartedAt}
	}
	for _, id := range client.completedBashOrder {
		if event := client.completedBashEvents[id]; event != nil {
			result.CompletedBashEvents = append(result.CompletedBashEvents, event)
		}
	}
	result.Compacting = client.compacting
	result.CompactingSince = copyTime(client.compactingSince)
	if len(client.queuedMessages["steering"]) > 0 || len(client.queuedMessages["followUp"]) > 0 {
		result.QueuedMessages = map[string][]string{"steering": append([]string(nil), client.queuedMessages["steering"]...), "followUp": append([]string(nil), client.queuedMessages["followUp"]...)}
	}
	if len(client.pendingDialogs) > 0 || len(client.extensionStatuses) > 0 || len(client.extensionWidgets) > 0 || client.extensionTitle != nil {
		dialogs := []map[string]any{}
		for _, id := range client.pendingDialogOrder {
			if dialog := client.pendingDialogs[id]; dialog != nil && !dialog.answering {
				dialogs = append(dialogs, client.snapshotDialogLocked(dialog))
			}
		}
		statuses := []map[string]any{}
		for _, key := range client.extensionStatusOrder {
			if item := client.extensionStatuses[key]; item != nil {
				statuses = append(statuses, item)
			}
		}
		widgets := []map[string]any{}
		for _, key := range client.extensionWidgetOrder {
			if item := client.extensionWidgets[key]; item != nil {
				widgets = append(widgets, item)
			}
		}
		result.ExtensionUI = map[string]any{"pending_dialogs": dialogs, "statuses": statuses, "widgets": widgets, "title": client.extensionTitle}
	}
	return result
}

func (client *Client) Close() error {
	client.closeMu.Lock()
	defer client.closeMu.Unlock()
	if client.closed {
		return nil
	}
	if client.stdin != nil {
		_ = client.stdin.Close()
	}
	var result error
	if client.process != nil {
		result = client.terminateProcess()
	}
	if client.stdout != nil {
		_ = client.stdout.Close()
	}
	if client.stderr != nil {
		_ = client.stderr.Close()
	}
	select {
	case <-client.readerDone:
	case <-time.After(200 * time.Millisecond):
	}
	select {
	case <-client.stderrDone:
	case <-time.After(200 * time.Millisecond):
	}
	if result == nil {
		client.closed = true
	}
	return result
}

func (client *Client) waitForProcess() {
	<-client.readerDone
	<-client.stderrDone
	_ = client.process.Wait()
	if client.processGroup != nil {
		_ = client.processGroup.Close()
	}
	close(client.waitDone)
}

func (client *Client) terminateProcess() error {
	select {
	case <-client.waitDone:
		return nil
	default:
	}
	client.diagnostics.Log("process_terminating", map[string]any{"pid": client.pid, "signal": "TERM", "process_group": true})
	if client.processGroup != nil {
		_ = client.processGroup.Signal(false)
	}
	select {
	case <-client.waitDone:
		return nil
	case <-time.After(client.processTermTimeout):
		client.diagnostics.Log("process_terminating", map[string]any{"pid": client.pid, "signal": "KILL", "process_group": true})
		if client.processGroup != nil {
			_ = client.processGroup.Signal(true)
		}
	}
	select {
	case <-client.waitDone:
		return nil
	case <-time.After(client.processKillTimeout):
		return errors.New("Pi RPC process did not exit after KILL")
	}
}

func (client *Client) updateQueuedMessagesLocked(response map[string]any) {
	if response["type"] != "queue_update" {
		return
	}
	perTypeCount := MaxQueuedMessageCount / 2
	perTypeBytes := (MaxQueuedMessageBytes - 128) / 2
	client.queuedMessages = map[string][]string{
		"steering": boundedStringArray(response["steering"], perTypeCount, perTypeBytes),
		"followUp": boundedStringArray(response["followUp"], perTypeCount, perTypeBytes),
	}
}

func (client *Client) updateExtensionUILocked(response map[string]any) {
	if response["type"] != "extension_ui_request" {
		return
	}
	client.pruneExpiredDialogsLocked()
	switch response["method"] {
	case "select", "confirm", "input", "editor":
		id := stringValue(response["id"])
		if id == "" || len(id) > 512 {
			return
		}
		if existing := client.pendingDialogs[id]; existing != nil && existing.answering {
			return
		}
		dialog := &extensionDialog{event: boundedExtensionUIEvent(response)}
		if timeout, ok := numberValue(response["timeout"]); ok && timeout > 0 {
			dialog.expiresAt = client.now().Add(time.Duration(timeout * float64(time.Millisecond)))
		}
		client.pendingDialogs[id] = dialog
		client.pendingDialogOrder = touchOrder(client.pendingDialogOrder, id)
		for len(client.pendingDialogOrder) > MaxExtensionUIItems {
			client.pendingDialogOrder = evictOldest(client.pendingDialogOrder, client.pendingDialogs)
		}
	case "setStatus":
		key := stringValue(response["statusKey"])
		if key == "" || len(key) > 512 {
			return
		}
		if response["statusText"] == nil || response["statusText"] == "" {
			delete(client.extensionStatuses, key)
			client.extensionStatusOrder = removeOrder(client.extensionStatusOrder, key)
		} else {
			client.extensionStatuses[key] = boundedExtensionUIEvent(response)
			client.extensionStatusOrder = touchOrder(client.extensionStatusOrder, key)
			for len(client.extensionStatusOrder) > MaxExtensionUIItems {
				client.extensionStatusOrder = evictOldest(client.extensionStatusOrder, client.extensionStatuses)
			}
		}
	case "setWidget":
		key := stringValue(response["widgetKey"])
		if key == "" || len(key) > 512 {
			return
		}
		if _, ok := response["widgetLines"].([]any); ok {
			client.extensionWidgets[key] = boundedExtensionUIEvent(response)
			client.extensionWidgetOrder = touchOrder(client.extensionWidgetOrder, key)
			for len(client.extensionWidgetOrder) > MaxExtensionUIItems {
				client.extensionWidgetOrder = evictOldest(client.extensionWidgetOrder, client.extensionWidgets)
			}
		} else {
			delete(client.extensionWidgets, key)
			client.extensionWidgetOrder = removeOrder(client.extensionWidgetOrder, key)
		}
	case "setTitle":
		if response["title"] == nil {
			client.extensionTitle = nil
		} else {
			client.extensionTitle = boundedExtensionUIEvent(response)
		}
	}
	client.boundExtensionUISnapshotLocked()
}

func boundedStringArray(value any, maxCount, maxBytes int) []string {
	raw, _ := value.([]any)
	result := make([]string, 0, min(len(raw), maxCount))
	used := 0
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			continue
		}
		text = boundedText(text, MaxSnapshotStringBytes)
		if len(result) >= maxCount || used+len(text)+3 > maxBytes {
			break
		}
		result = append(result, text)
		used += len(text) + 3
	}
	return result
}

func boundedExtensionUIEvent(response map[string]any) map[string]any {
	result := map[string]any{"type": "extension_ui_request", "method": response["method"]}
	copyString := func(key string, limit int) {
		if value, ok := response[key].(string); ok {
			result[key] = boundedText(value, limit)
		}
	}
	for _, key := range []string{"id", "statusKey", "widgetKey"} {
		copyString(key, 512)
	}
	for _, key := range []string{"title", "message", "placeholder", "value", "statusText"} {
		copyString(key, MaxSnapshotStringBytes/4)
	}
	if timeout, ok := numberValue(response["timeout"]); ok {
		result["timeout"] = timeout
	}
	if options, ok := response["options"].([]any); ok {
		values := make([]any, 0, min(len(options), 16))
		for _, option := range options {
			if text, ok := option.(string); ok && len(values) < 16 {
				values = append(values, boundedText(text, 512))
			}
		}
		result["options"] = values
	}
	if lines, ok := response["widgetLines"].([]any); ok {
		values := make([]any, 0, min(len(lines), 16))
		for _, line := range lines {
			if text, ok := line.(string); ok && len(values) < 16 {
				values = append(values, boundedText(text, 512))
			}
		}
		result["widgetLines"] = values
	}
	for _, key := range []string{"confirmed", "cancelled"} {
		if value, ok := response[key].(bool); ok {
			result[key] = value
		}
	}
	if jsonSize(result) > MaxExtensionUIItemBytes {
		for _, key := range []string{"message", "value", "placeholder", "title", "options", "widgetLines"} {
			delete(result, key)
			if jsonSize(result) <= MaxExtensionUIItemBytes {
				break
			}
		}
	}
	return result
}

func touchOrder(order []string, key string) []string {
	return append(removeOrder(order, key), key)
}

func removeOrder(order []string, key string) []string {
	for index, candidate := range order {
		if candidate == key {
			return append(order[:index], order[index+1:]...)
		}
	}
	return order
}

func evictOldest[T any](order []string, values map[string]T) []string {
	if len(order) == 0 {
		return order
	}
	delete(values, order[0])
	return order[1:]
}

func (client *Client) boundExtensionUISnapshotLocked() {
	for client.extensionUIBytesLocked() > MaxExtensionUISnapshotBytes {
		switch {
		case len(client.pendingDialogOrder) > 0:
			client.pendingDialogOrder = evictOldest(client.pendingDialogOrder, client.pendingDialogs)
		case len(client.extensionStatusOrder) > 0:
			client.extensionStatusOrder = evictOldest(client.extensionStatusOrder, client.extensionStatuses)
		case len(client.extensionWidgetOrder) > 0:
			client.extensionWidgetOrder = evictOldest(client.extensionWidgetOrder, client.extensionWidgets)
		case client.extensionTitle != nil:
			client.extensionTitle = nil
		default:
			return
		}
	}
}

func (client *Client) extensionUIBytesLocked() int {
	bytes := jsonSize(client.extensionTitle) + 128
	for _, dialog := range client.pendingDialogs {
		bytes += jsonSize(dialog.event)
	}
	for _, status := range client.extensionStatuses {
		bytes += jsonSize(status)
	}
	for _, widget := range client.extensionWidgets {
		bytes += jsonSize(widget)
	}
	return bytes
}

func (client *Client) pruneExpiredDialogsLocked() {
	now := client.now()
	for id, dialog := range client.pendingDialogs {
		if !dialog.expiresAt.IsZero() && !dialog.expiresAt.After(now) {
			delete(client.pendingDialogs, id)
			client.pendingDialogOrder = removeOrder(client.pendingDialogOrder, id)
		}
	}
}
func (client *Client) snapshotDialogLocked(dialog *extensionDialog) map[string]any {
	if dialog.expiresAt.IsZero() {
		return dialog.event
	}
	result := cloneMap(dialog.event)
	remaining := dialog.expiresAt.Sub(client.now())
	if remaining < 0 {
		remaining = 0
	}
	result["timeout"] = int64(math.Ceil(float64(remaining) / float64(time.Millisecond)))
	return result
}
func (client *Client) extensionUIEventForDeliveryLocked(event map[string]any) map[string]any {
	if event["type"] != "extension_ui_request" {
		return event
	}
	method := stringValue(event["method"])
	if method != "select" && method != "confirm" && method != "input" && method != "editor" {
		return event
	}
	dialog := client.pendingDialogs[stringValue(event["id"])]
	if dialog == nil || dialog.answering {
		return nil
	}
	return client.snapshotDialogLocked(dialog)
}

func internalBridgeStatusKey(response map[string]any) string {
	if response["type"] != "extension_ui_request" || response["method"] != "setStatus" {
		return ""
	}
	key := stringValue(response["statusKey"])
	matched, _ := regexp.MatchString(`(?i)^gripi_tree_(snapshot|leaf|navigate|label):[a-f0-9]+$`, key)
	if matched {
		return key
	}
	return ""
}

func replayCoalesceKey(response map[string]any) string {
	switch response["type"] {
	case "message_update":
		return "message_update"
	case "tool_execution_update":
		id := stringValue(response["toolCallId"])
		if id != "" && len(id) <= MaxActiveToolSnapshotIDBytes {
			return "tool_update:" + id
		}
	}
	return ""
}
func (client *Client) discardSupersededLocked(response map[string]any) {
	switch response["type"] {
	case "message_start", "message_end":
		client.removeCoalescedLocked("message_update")
	case "tool_execution_start", "tool_execution_end":
		id := stringValue(response["toolCallId"])
		if id != "" {
			client.removeCoalescedLocked("tool_update:" + id)
		}
	case "agent_end":
		for key := range client.coalesced {
			client.removeCoalescedLocked(key)
		}
	}
}
func (client *Client) appendReplayLocked(event map[string]any, size int, key string) {
	if key != "" {
		client.removeCoalescedLocked(key)
	}
	entry := replayEntry{sequence: client.eventSequence, event: event, bytes: size, key: key}
	client.events = append(client.events, entry)
	client.eventBufferSize += size
	if key != "" {
		client.coalesced[key] = &client.events[len(client.events)-1]
	}
	for len(client.events) > client.eventBufferLimit || client.eventBufferSize > client.eventBufferBytes {
		removed := client.events[0]
		client.events = client.events[1:]
		client.eventBufferSize -= removed.bytes
		if removed.sequence > client.eventReplayFloor {
			client.eventReplayFloor = removed.sequence
		}
		if removed.key != "" {
			delete(client.coalesced, removed.key)
		}
		client.rebuildCoalescedPointersLocked()
	}
}
func (client *Client) removeCoalescedLocked(key string) {
	if key == "" {
		return
	}
	target := client.coalesced[key]
	if target == nil {
		return
	}
	for index := range client.events {
		if client.events[index].sequence == target.sequence {
			client.eventBufferSize -= client.events[index].bytes
			client.events = append(client.events[:index], client.events[index+1:]...)
			break
		}
	}
	delete(client.coalesced, key)
	client.rebuildCoalescedPointersLocked()
}
func (client *Client) rebuildCoalescedPointersLocked() {
	for key := range client.coalesced {
		delete(client.coalesced, key)
	}
	for index := range client.events {
		if key := client.events[index].key; key != "" {
			client.coalesced[key] = &client.events[index]
		}
	}
}
func (client *Client) discardReplayLocked() {
	client.events = nil
	client.coalesced = make(map[string]*replayEntry)
	client.eventBufferSize = 0
	client.eventReplayFloor = client.eventSequence
}

func (client *Client) updateActiveToolsLocked(response map[string]any, serializedBytes int) {
	if response["type"] == "agent_end" {
		client.activeToolEvents = make(map[string]map[string]any)
		return
	}
	id := stringValue(response["toolCallId"])
	if id == "" || len(id) > MaxActiveToolSnapshotIDBytes {
		return
	}
	if response["type"] == "tool_execution_end" {
		delete(client.activeToolEvents, id)
		return
	}
	if (response["type"] == "tool_execution_start" || response["type"] == "tool_execution_update") && response["toolName"] == snapshotToolName {
		if client.activeToolEvents[id] == nil && len(client.activeToolEvents) >= MaxActiveToolSnapshots {
			return
		}
		if snapshot := boundedActiveToolEvent(response, serializedBytes); snapshot != nil {
			client.activeToolEvents[id] = snapshot
		}
	}
}
func (client *Client) boundedReplayEvent(response map[string]any, serializedBytes int) (map[string]any, int) {
	if response["type"] != "tool_execution_update" || response["toolName"] != snapshotToolName || serializedBytes <= MaxActiveToolSnapshotBytes {
		return response, serializedBytes
	}
	event := boundedActiveToolEvent(response, serializedBytes)
	if event == nil {
		return nil, 0
	}
	return event, jsonSize(event)
}

func boundedActiveToolEvent(response map[string]any, serializedBytes int) map[string]any {
	if serializedBytes <= MaxActiveToolSnapshotBytes {
		return response
	}
	result, _ := response["partialResult"].(map[string]any)
	details, _ := result["details"].(map[string]any)
	if _, tools := details["tools"].([]any); tools {
		if _, usage := details["usage"].(map[string]any); usage {
			snapshot := generalSubagentSnapshot(response, result, details)
			if jsonSize(snapshot) <= MaxActiveToolSnapshotBytes {
				return snapshot
			}
		}
	}
	fallback := subagentFallbackSnapshot(response, result)
	if jsonSize(fallback) <= MaxActiveToolSnapshotBytes {
		return fallback
	}
	return nil
}
func generalSubagentSnapshot(response, result, details map[string]any) map[string]any {
	compact := map[string]any{}
	for key, limit := range map[string]int{"task": activeToolSnapshotTextBytes, "model": 512, "status": 256, "streamingText": activeToolSnapshotTextBytes} {
		if details[key] != nil {
			compact[key] = boundedText(fmt.Sprint(details[key]), limit)
		}
	}
	if tools, ok := details["tools"].([]any); ok {
		start := max(0, len(tools)-activeToolSnapshotToolLimit)
		values := []any{}
		for _, raw := range tools[start:] {
			if tool, ok := raw.(map[string]any); ok {
				item := map[string]any{}
				if tool["name"] != nil {
					item["name"] = boundedText(fmt.Sprint(tool["name"]), 256)
				}
				if args, ok := tool["args"].(map[string]any); ok {
					item["args"] = compactValues(args)
				}
				if tool["status"] != nil {
					item["status"] = boundedText(fmt.Sprint(tool["status"]), 256)
				}
				if tool["output"] != nil {
					item["output"] = boundedText(fmt.Sprint(tool["output"]), activeToolSnapshotOutputBytes)
				}
				values = append(values, item)
			}
		}
		compact["tools"] = values
	}
	if texts, ok := details["textItems"].([]any); ok && len(texts) > 0 {
		compact["textItems"] = []any{boundedText(fmt.Sprint(texts[len(texts)-1]), activeToolSnapshotTextBytes)}
	}
	if usage, ok := details["usage"].(map[string]any); ok {
		compact["usage"] = compactValues(usage)
	}
	return map[string]any{"type": "tool_execution_update", "toolCallId": response["toolCallId"], "toolName": snapshotToolName, "partialResult": map[string]any{"content": compactContent(result["content"]), "details": compact}}
}
func compactContent(raw any) []any {
	values, _ := raw.([]any)
	if len(values) == 0 {
		return []any{}
	}
	part, _ := values[len(values)-1].(map[string]any)
	if part["type"] != "text" {
		return []any{}
	}
	return []any{map[string]any{"type": "text", "text": boundedText(stringValue(part["text"]), activeToolSnapshotTextBytes)}}
}
func compactValues(values map[string]any) map[string]any {
	result := map[string]any{}
	count := 0
	for key, value := range values {
		if count >= 12 {
			break
		}
		switch typed := value.(type) {
		case string:
			result[key] = boundedText(typed, 512)
		case float64:
			if !math.IsInf(typed, 0) && !math.IsNaN(typed) {
				result[key] = typed
			}
		case nil, bool, json.Number:
			result[key] = typed
		case int, int64:
			result[key] = typed
		}
		count++
	}
	return result
}
func subagentFallbackSnapshot(response, result map[string]any) map[string]any {
	content := compactContent(result["content"])
	text := ""
	if len(content) > 0 {
		text = stringValue(content[0].(map[string]any)["text"])
	}
	if text == "" {
		text = "Subagent is still running…"
	}
	return map[string]any{"type": "tool_execution_update", "toolCallId": response["toolCallId"], "toolName": snapshotToolName, "partialResult": map[string]any{"content": []any{map[string]any{"type": "text", "text": text}}}}
}
func boundedText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	omission := "\n…\n"
	available := limit - len(omission)
	head := validPrefix(value, available/2)
	tail := validSuffix(value, available-available/2)
	return head + omission + tail
}
func validPrefix(value string, limit int) string {
	if limit >= len(value) {
		return value
	}
	for limit > 0 && !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit]
}
func validSuffix(value string, limit int) string {
	if limit >= len(value) {
		return value
	}
	start := len(value) - limit
	for start < len(value) && !utf8.ValidString(value[start:]) {
		start++
	}
	return value[start:]
}

func (client *Client) updateBusyStateLocked(response map[string]any) {
	typeName := stringValue(response["type"])
	when := time.UnixMilli(int64(numberOrZero(response["gatewayTimestamp"])))
	switch typeName {
	case "agent_start":
		client.agentRunning = true
		client.settledAt = nil
		client.busy = true
		if client.busySince == nil {
			client.busySince = &when
		}
	case "compaction_start":
		client.compacting = true
		if client.compactingSince == nil {
			client.compactingSince = &when
		}
		client.busy = true
		if client.busySince == nil {
			copy := *client.compactingSince
			client.busySince = &copy
		}
	case "turn_start":
		client.busy = true
		if client.busySince == nil {
			client.busySince = &when
		}
	case "turn_end":
		if !client.agentRunning {
			client.busy = false
			client.busySince = nil
		}
	case "agent_settled":
		client.agentRunning = false
		client.completedBashEvents = make(map[string]map[string]any)
		client.completedBashOrder = nil
		client.settledAt = &when
		client.busy = false
		client.busySince = nil
	case "compaction", "compaction_end":
		client.compacting = false
		client.compactingSince = nil
		if !client.agentRunning {
			client.busy = false
			client.busySince = nil
		}
	}
}
func (client *Client) busySinceLocked() *time.Time {
	if client.busySince == nil {
		return client.activeBashStartedAt
	}
	if client.activeBashStartedAt == nil || client.busySince.Before(*client.activeBashStartedAt) {
		return client.busySince
	}
	return client.activeBashStartedAt
}

func (client *Client) Bash(ctx context.Context, command string, exclude bool) (response map[string]any, err error) {
	token := &bashToken{id: randomID("bash-"), command: command, excludeFromContext: exclude, startedAt: client.clock()}
	client.mu.Lock()
	if client.activeBashToken != nil {
		client.mu.Unlock()
		return nil, ErrBashAlreadyRunning
	}
	client.activeBashToken = token
	client.mu.Unlock()
	defer func() {
		client.mu.Lock()
		if client.activeBashToken == token {
			client.clearActiveBashLocked()
		}
		client.mu.Unlock()
	}()
	payload := map[string]any{"command": command}
	if exclude {
		payload["excludeFromContext"] = true
	}
	response, err = client.request(ctx, "bash", token.id, payload, 0, func() { client.startBash(token) })
	if err != nil {
		if token.started {
			client.completeBash(token, "bash_error", "error", err.Error())
			return nil, &BashRequestError{BashID: token.id, Err: err}
		}
		return nil, err
	}
	if response["success"] == true {
		client.completeBash(token, "bash_end", "result", response["data"])
	} else {
		message := stringValue(response["error"])
		if message == "" {
			message = "Bash command failed"
		}
		client.completeBash(token, "bash_error", "error", message)
	}
	return response, nil
}
func (client *Client) startBash(token *bashToken) {
	client.mu.Lock()
	defer client.mu.Unlock()
	if token.started {
		return
	}
	token.started = true
	if client.activeBashToken == token {
		client.activeBashID = token.id
		client.activeBashCommand = token.command
		client.activeBashExclude = token.excludeFromContext
		started := token.startedAt
		client.activeBashStartedAt = &started
	}
	client.appendGatewayEventLocked(map[string]any{"type": "bash_start", "bashId": token.id, "command": token.command, "excludeFromContext": token.excludeFromContext, "gatewayTimestamp": token.startedAt.UnixMilli()})
}
func (client *Client) completeBash(token *bashToken, typeName, valueKey string, value any) {
	client.mu.Lock()
	defer client.mu.Unlock()
	active := client.activeBashToken == token
	id := token.id
	if active {
		id = client.activeBashID
		client.clearActiveBashLocked()
	}
	event := map[string]any{"type": typeName, "bashId": id, "command": token.command, "excludeFromContext": token.excludeFromContext, valueKey: value, "startedAt": token.startedAt.UnixMilli(), "gatewayTimestamp": client.clock().UnixMilli()}
	if active && client.agentRunning {
		client.completedBashEvents[id] = event
		client.completedBashOrder = append(client.completedBashOrder, id)
		for len(client.completedBashOrder) > MaxCompletedBashEvents {
			delete(client.completedBashEvents, client.completedBashOrder[0])
			client.completedBashOrder = client.completedBashOrder[1:]
		}
	}
	client.appendGatewayEventLocked(event)
}
func (client *Client) clearActiveBashLocked() {
	client.activeBashToken = nil
	client.activeBashID = ""
	client.activeBashCommand = ""
	client.activeBashExclude = false
	client.activeBashStartedAt = nil
}
func (client *Client) appendGatewayEventLocked(event map[string]any) {
	client.eventSequence++
	client.appendReplayLocked(event, jsonSize(event), "")
}

func (client *Client) waitForCompactionFlush(ctx context.Context, deadline time.Time) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		client.mu.Lock()
		waiting := client.flushingCompactionFollowUps || (client.compacting && len(client.compactionFollowUps) > 0)
		client.mu.Unlock()
		if !waiting {
			return nil
		}
		remaining := deadline.Sub(client.now())
		if remaining <= 0 {
			return &RequestTimeoutError{Command: "prompt"}
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-client.readerDone:
			timer.Stop()
			return ErrProcessExited
		case <-timer.C:
			return &RequestTimeoutError{Command: "prompt"}
		case <-ticker.C:
			timer.Stop()
		}
	}
}
func (client *Client) flushCompactionFollowUps(items []map[string]any, firstType string) {
	deadline := client.now().Add(client.requestTimeout)
	timer := time.NewTimer(max(client.requestTimeout, time.Nanosecond))
	defer timer.Stop()
	select {
	case <-client.writeLane:
		defer func() { client.writeLane <- struct{}{} }()
	case <-client.readerDone:
		client.finishFailedCompactionFlush("")
		return
	case <-timer.C:
		client.finishFailedCompactionFlush("")
		return
	}
	for {
		for index, payload := range items {
			command := "follow_up"
			if index == 0 && firstType != "" {
				command = firstType
			}
			id := client.nextID(command)
			client.mu.Lock()
			client.deferredCommandIDs[id] = true
			client.mu.Unlock()
			value := cloneMap(payload)
			value["id"], value["type"] = id, command
			if err := client.writeCommandUnlocked(value, command, deadline); err != nil {
				client.finishFailedCompactionFlush(id)
				return
			}
			size := promptPayloadSize(payload)
			client.mu.Lock()
			if !client.flushingCompactionFollowUps {
				client.mu.Unlock()
				return
			}
			client.compactionFollowUpCount--
			client.compactionFollowUpBytes -= size
			client.mu.Unlock()
		}
		client.mu.Lock()
		if len(client.compactionFollowUps) == 0 {
			client.flushingCompactionFollowUps = false
			client.mu.Unlock()
			return
		}
		items, client.compactionFollowUps = client.compactionFollowUps, nil
		firstType = "follow_up"
		client.mu.Unlock()
		if !client.now().Before(deadline) {
			client.finishFailedCompactionFlush("")
			return
		}
	}
}

func (client *Client) finishFailedCompactionFlush(_ string) {
	client.mu.Lock()
	client.deferredCommandIDs = make(map[string]bool)
	client.compactionFollowUps = nil
	client.compactionFollowUpCount = 0
	client.compactionFollowUpBytes = 0
	client.flushingCompactionFollowUps = false
	client.mu.Unlock()
}

func (client *Client) ExtensionUIResponse(ctx context.Context, id string, value *string, confirmed *bool, cancelled bool) (map[string]any, error) {
	command := map[string]any{"type": "extension_ui_response", "id": id}
	if cancelled {
		command["cancelled"] = true
	} else if confirmed != nil {
		command["confirmed"] = *confirmed
	} else if value != nil {
		command["value"] = *value
	} else {
		command["value"] = ""
	}
	client.mu.Lock()
	client.pruneExpiredDialogsLocked()
	dialog := client.pendingDialogs[id]
	if dialog != nil && !dialog.answering {
		dialog.answering = true
	} else {
		dialog = nil
	}
	client.mu.Unlock()
	if dialog == nil {
		return map[string]any{"type": "response", "command": "extension_ui_response", "success": false, "error": "Extension UI request is no longer pending"}, nil
	}
	deadline := client.now().Add(client.requestTimeout)
	if err := client.writeCommand(ctx, command, "extension_ui_response", deadline); err != nil {
		client.mu.Lock()
		if client.pendingDialogs[id] == dialog {
			dialog.answering = false
			client.eventSequence++
			client.appendReplayLocked(dialog.event, jsonSize(dialog.event), "")
		}
		client.mu.Unlock()
		return nil, err
	}
	client.mu.Lock()
	delete(client.pendingDialogs, id)
	client.pendingDialogOrder = removeOrder(client.pendingDialogOrder, id)
	client.mu.Unlock()
	return map[string]any{"type": "response", "command": "extension_ui_response", "success": true}, nil
}

func (client *Client) TreeSnapshot(ctx context.Context, filter string) (map[string]any, error) {
	payload := map[string]any{}
	if filter != "" {
		payload["filter"] = filter
	}
	return client.extensionRequest(ctx, "gripi_tree_snapshot", payload, client.treeBridgeTimeout, "Session tree request timed out")
}
func (client *Client) TreeLeaf(ctx context.Context) (map[string]any, error) {
	return client.extensionRequest(ctx, "gripi_tree_leaf", map[string]any{}, client.treeBridgeTimeout, "Session tree request timed out")
}
func (client *Client) NavigateTree(ctx context.Context, entryID, summary, instructions string) (map[string]any, error) {
	payload := map[string]any{"entryId": entryID, "summary": summary}
	if instructions != "" {
		payload["customInstructions"] = instructions
	}
	return client.extensionRequest(ctx, "gripi_tree_navigate", payload, LongRequestTimeout, "Extension command timed out")
}
func (client *Client) SetTreeLabel(ctx context.Context, entryID, label string) (map[string]any, error) {
	label = strings.TrimSpace(label)
	payload := map[string]any{"entryId": entryID, "label": nil}
	if label != "" {
		payload["label"] = label
	}
	return client.extensionRequest(ctx, "gripi_tree_label", payload, client.requestTimeout, "Extension command timed out")
}
func (client *Client) extensionRequest(ctx context.Context, command string, payload map[string]any, timeout time.Duration, timeoutMessage string) (map[string]any, error) {
	requestID := randomID("")
	key := command + ":" + requestID
	encoded, _ := marshalJSON(payload)
	message := "/" + command + " " + requestID + " " + base64.RawURLEncoding.EncodeToString(encoded)
	status := make(chan string, 1)
	client.mu.Lock()
	client.bridgePending[key] = status
	client.mu.Unlock()
	defer func() { client.mu.Lock(); delete(client.bridgePending, key); client.mu.Unlock() }()
	deadline := client.now().Add(timeout)
	response, err := client.request(ctx, "prompt", client.nextID("prompt"), map[string]any{"message": message}, timeout, nil)
	if err != nil {
		var timeoutErr *RequestTimeoutError
		if errors.As(err, &timeoutErr) {
			return map[string]any{"success": false, "error": timeoutMessage}, nil
		}
		return nil, err
	}
	if response["success"] == false {
		return response, nil
	}
	remaining := deadline.Sub(client.now())
	if remaining <= 0 {
		return map[string]any{"success": false, "error": timeoutMessage}, nil
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case raw := <-status:
		var result map[string]any
		if json.Unmarshal([]byte(raw), &result) != nil {
			return mergeResponse(response, map[string]any{"success": false, "error": "Extension command returned an invalid response"}), nil
		}
		if result["ok"] != true {
			message := stringValue(result["error"])
			if message == "" {
				message = "Extension command failed"
			}
			return mergeResponse(response, map[string]any{"success": false, "error": message}), nil
		}
		delete(result, "ok")
		return mergeResponse(response, map[string]any{"data": result}), nil
	case <-timer.C:
		return map[string]any{"success": false, "error": timeoutMessage}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-client.readerDone:
		return nil, ErrProcessExited
	}
}

func isTimeout(err error) bool { var target *RequestTimeoutError; return errors.As(err, &target) }
func isNetTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	var target timeout
	return errors.As(err, &target) && target.Timeout()
}
func marshalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}
func jsonSize(value any) int { encoded, _ := marshalJSON(value); return len(encoded) }
func promptPayloadSize(payload map[string]any) int {
	images, _ := payload["images"].([]PromptImage)
	if len(images) == 0 {
		return jsonSize(payload)
	}
	copy := cloneMap(payload)
	delete(copy, "images")
	size := jsonSize(copy) + len(`,"images":[]`)
	for _, image := range images {
		size += base64.StdEncoding.EncodedLen(int(image.Size)) + len(image.MIMEType) + 48
	}
	return size
}
func cloneMap(value map[string]any) map[string]any {
	copy := make(map[string]any, len(value))
	for key, item := range value {
		copy[key] = item
	}
	return copy
}
func stringValue(value any) string { result, _ := value.(string); return result }
func stringArray(value any) []string {
	raw, _ := value.([]any)
	result := []string{}
	for _, item := range raw {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}
func numberValue(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case json.Number:
		value, err := typed.Float64()
		return value, err == nil
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	}
	return 0, false
}
func numberOrZero(value any) float64 { result, _ := numberValue(value); return result }
func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
func mergeResponse(response, extra map[string]any) map[string]any {
	result := cloneMap(response)
	for key, value := range extra {
		result[key] = value
	}
	return result
}
func randomID(prefix string) string {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err == nil {
		return fmt.Sprintf("%s%x", prefix, value)
	}
	return fmt.Sprintf("%s%x", prefix, time.Now().UnixNano())
}
