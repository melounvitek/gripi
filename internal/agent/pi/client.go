package pi

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/melounvitek/gripi/internal/agent"
	"github.com/melounvitek/gripi/internal/rpc"
)

// Client serves a session through Pi's RPC mode.
type Client struct{ *rpc.Client }

// The gateway finds the optional interfaces by type assertion and falls back without them, so check them at compile
// time on the client that Start returns.
var (
	_ agent.Client                   = (*Client)(nil)
	_ agent.CompactionPromptDeferrer = (*Client)(nil)
	_ agent.QueueRestorer            = (*Client)(nil)
	_ agent.QueueClearer             = (*Client)(nil)
	_ agent.InterruptStater          = (*Client)(nil)
	_ agent.Forker                   = (*Client)(nil)
	_ agent.Cloner                   = (*Client)(nil)
	_ agent.Compacter                = (*Client)(nil)
	_ agent.Renamer                  = (*Client)(nil)
	_ agent.Reloader                 = (*Client)(nil)
	_ rpc.ActionClient               = (*Client)(nil)
	_ rpc.SessionEntriesReader       = (*Client)(nil)
)

// start runs a Pi RPC client whose events name the Client that serves it, even those Pi sends before run returns.
func start(observe func(agent.Client, map[string]any), run func(observer func(*rpc.Client, map[string]any)) (*rpc.Client, error)) (agent.Client, error) {
	client := &Client{}
	var observer func(*rpc.Client, map[string]any)
	if observe != nil {
		observer = func(_ *rpc.Client, event map[string]any) { observe(client, event) }
	}
	rpcClient, err := run(observer)
	if err != nil {
		return nil, err
	}
	client.Client = rpcClient
	return client, nil
}

func (client *Client) State(ctx context.Context) (agent.State, error) {
	return stateOf(client.GetState(ctx))
}

func (client *Client) InterruptState(ctx context.Context) (agent.State, error) {
	return stateOf(client.GetStateForInterrupt(ctx))
}

func stateOf(response map[string]any, err error) (agent.State, error) {
	if err != nil {
		return agent.State{}, err
	}
	if err := refusal(response); err != nil {
		return agent.State{}, err
	}
	return stateFrom(response), nil
}

func stateFrom(response map[string]any) agent.State {
	data := responseData(response)
	state := agent.State{ID: stringValue(data["sessionId"]), Name: stringValue(data["sessionName"]), Streaming: data["isStreaming"] == true, Compacting: data["isCompacting"] == true}
	for _, key := range []string{"sessionFile", "session_file", "path"} {
		if state.Key = stringValue(data[key]); state.Key != "" {
			break
		}
	}
	return state
}

// LiveStatus leaves unknown what Pi refuses to tell.
func (client *Client) LiveStatus(ctx context.Context) (agent.LiveStatus, error) {
	stateResponse, err := client.GetState(ctx)
	if err != nil {
		return agent.LiveStatus{}, err
	}
	statsResponse, err := client.GetSessionStats(ctx)
	if err != nil {
		return agent.LiveStatus{}, err
	}
	state := successfulData(stateResponse)
	status := agent.LiveStatus{ThinkingLevel: stringValue(state["thinkingLevel"])}
	if model, ok := state["model"].(map[string]any); ok {
		status.Provider, status.ModelID = stringValue(model["provider"]), stringValue(model["id"])
	}
	if usage, ok := successfulData(statsResponse)["contextUsage"].(map[string]any); ok {
		status.Context = contextUsage(usage)
	}
	return status, nil
}

// contextUsage is nil when a value Pi reports is not a number. Pi reports no tokens just after a compaction.
func contextUsage(usage map[string]any) *agent.ContextUsage {
	for _, key := range []string{"tokens", "contextWindow", "percent"} {
		if _, ok := number(usage[key]); usage[key] != nil && !ok {
			return nil
		}
	}
	result := &agent.ContextUsage{}
	result.Tokens, result.HasTokens = number(usage["tokens"])
	result.Window, result.HasWindow = number(usage["contextWindow"])
	result.Percent, _ = number(usage["percent"])
	return result
}

func (client *Client) Commands(ctx context.Context) ([]agent.Command, error) {
	response, err := client.GetCommands(ctx)
	if err != nil {
		return nil, err
	}
	if err := refusal(response); err != nil {
		return nil, err
	}
	return commandsFrom(response), nil
}

// commandsFrom lists the commands Pi reports, once each, without those that the gateway runs itself or that carry
// its own requests.
func commandsFrom(response map[string]any) []agent.Command {
	seen := make(map[string]bool)
	for _, command := range builtinCommands {
		seen[command.Name] = true
	}
	raw, _ := responseData(response)["commands"].([]any)
	var commands []agent.Command
	for _, item := range raw {
		command, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := stringValue(command["name"])
		if name == "" || seen[name] || rpc.InternalCommand("/"+name) {
			continue
		}
		seen[name] = true
		group := stringValue(command["source"])
		if group == "" {
			group = "other"
		}
		commands = append(commands, agent.Command{Name: name, Description: stringValue(command["description"]), Group: group})
	}
	return commands
}

// extensionCommand reports whether Pi runs the message as an extension's command, which takes no images and runs
// even during a compaction.
func (client *Client) extensionCommand(ctx context.Context, message string) bool {
	if !strings.HasPrefix(message, "/") {
		return false
	}
	name := strings.TrimPrefix(strings.SplitN(message, " ", 2)[0], "/")
	commands, err := client.Commands(ctx)
	if err != nil {
		return false
	}
	for _, command := range commands {
		if command.Name == name {
			return command.Group == "extension"
		}
	}
	return false
}

func (client *Client) Prompt(ctx context.Context, prompt agent.Prompt) (agent.PromptResult, error) {
	var images []agent.PromptImage
	if prompt.Images != nil && !client.extensionCommand(ctx, prompt.Text) {
		var err error
		if images, err = prompt.Images(); err != nil {
			return agent.PromptResult{}, err
		}
	}
	switch prompt.Delivery {
	case agent.Steer, agent.FollowUp:
		return promptResult(client.PromptWithBehavior(ctx, prompt.Text, images, streamingBehavior(prompt.Delivery)))
	default:
		return promptResult(client.Client.Prompt(ctx, prompt.Text, images))
	}
}

// DeferPrompt holds a steered or follow-up prompt back while Pi compacts, which Pi itself cannot. Pi does run an
// extension's command during a compaction, so the client sends that at once.
func (client *Client) DeferPrompt(ctx context.Context, prompt agent.Prompt) (agent.PromptResult, bool, error) {
	deferring := client.DeferringCompactionPrompts()
	command := (deferring || prompt.Images != nil) && client.extensionCommand(ctx, prompt.Text)
	var images []agent.PromptImage
	if prompt.Images != nil && !command {
		var err error
		if images, err = prompt.Images(); err != nil {
			return agent.PromptResult{}, false, err
		}
	}
	if deferring && command {
		result, err := promptResult(client.PromptWithBehavior(ctx, prompt.Text, images, streamingBehavior(prompt.Delivery)))
		if err == nil {
			result.Activity = &agent.Activity{Running: client.AgentRunning(), Compacting: client.Compacting()}
		}
		return result, true, err
	}
	response, queued, err := client.QueueCompactionPrompt(ctx, prompt.Text, images, streamingBehavior(prompt.Delivery))
	if err != nil || !queued {
		return agent.PromptResult{}, queued, err
	}
	if err := refusal(response); err != nil {
		return agent.PromptResult{}, true, err
	}
	return agent.PromptResult{QueuedAfterCompaction: response["compacting"] == true}, true, nil
}

func streamingBehavior(delivery agent.Delivery) string {
	if delivery == agent.FollowUp {
		return "followUp"
	}
	return "steer"
}

func promptResult(response map[string]any, err error) (agent.PromptResult, error) {
	if err := answer(response, err); err != nil {
		return agent.PromptResult{}, err
	}
	disposition, _ := responseData(response)["disposition"].(string)
	return agent.PromptResult{Disposition: agent.Disposition(disposition)}, nil
}

func (client *Client) Cancel(ctx context.Context) error {
	return answer(client.Abort(ctx))
}

func (client *Client) NewSession(ctx context.Context) (agent.Branch, error) {
	return client.branch(ctx, func() (map[string]any, error) { return client.Client.NewSession(ctx, "") })
}

func (client *Client) Clone(ctx context.Context) (agent.Branch, error) {
	return client.branch(ctx, func() (map[string]any, error) { return client.CloneSession(ctx) })
}

func (client *Client) Fork(ctx context.Context, entryID string) (agent.Branch, error) {
	return client.branch(ctx, func() (map[string]any, error) { return client.Client.Fork(ctx, entryID) })
}

// branch switches Pi to the session that switchSession creates, and asks Pi which session that is.
func (client *Client) branch(ctx context.Context, switchSession func() (map[string]any, error)) (agent.Branch, error) {
	response, err := switchSession()
	if err := answer(response, err); err != nil {
		return agent.Branch{}, err
	}
	data := responseData(response)
	if data["cancelled"] == true {
		return agent.Branch{Cancelled: true}, nil
	}
	stateResponse, err := client.GetState(ctx)
	if err != nil {
		return agent.Branch{}, err
	}
	// A refused state names no session, which fails the branch.
	state := stateFrom(stateResponse)
	branch := agent.Branch{Key: state.Key, ID: state.ID, Moved: true}
	if text, ok := data["text"].(string); ok {
		branch.Text = &text
	}
	return branch, nil
}

// ForkMessages lists none when Pi's answer has no messages, even when Pi refused the request.
func (client *Client) ForkMessages(ctx context.Context) (json.RawMessage, error) {
	response, err := client.GetForkMessages(ctx)
	if err != nil {
		return nil, err
	}
	messages, _ := responseData(response)["messages"].([]any)
	if messages == nil {
		messages = []any{}
	}
	return json.Marshal(messages)
}

func (client *Client) Compact(ctx context.Context, instructions string) error {
	return answer(client.Client.Compact(ctx, instructions))
}

func (client *Client) Rename(ctx context.Context, name string) error {
	return answer(client.SetSessionName(ctx, name))
}

func (client *Client) Reload(ctx context.Context) error {
	return answer(client.Client.Reload(ctx))
}

// answer returns the error of a request that failed or that Pi refused.
func answer(response map[string]any, err error) error {
	if err != nil {
		return err
	}
	return refusal(response)
}

func refusal(response map[string]any) error {
	if response["success"] == true {
		return nil
	}
	return &agent.RequestError{Message: stringValue(response["error"])}
}

// responseData is the data of Pi's answer, or the answer itself when it has none.
func responseData(response map[string]any) map[string]any {
	if data, ok := response["data"].(map[string]any); ok {
		return data
	}
	return response
}

func successfulData(response map[string]any) map[string]any {
	if response["success"] != true {
		return nil
	}
	data, _ := response["data"].(map[string]any)
	return data
}

func stringValue(value any) string {
	result, _ := value.(string)
	return result
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		result, err := typed.Float64()
		return result, err == nil
	default:
		return 0, false
	}
}
