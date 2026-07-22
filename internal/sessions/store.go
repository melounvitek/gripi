package sessions

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	MaxIndexedEntryBytes   = 256 << 10
	MaxRenderedEntryBytes  = 64 << 20
	MaxRetainedWindowBytes = 64 << 20
	WindowMinMessages      = 20
	WindowMaxMessages      = 150
	WindowByteBudget       = 128 << 10
	maxCacheEntries        = 256
	maxCacheBytes          = 32 << 20
	maxBuiltIndexBytes     = 64 << 20
)

var imageMIMETypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

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
}

type Image struct {
	Data     string
	MIMEType string
	Src      string
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

type segment struct {
	Role          string
	ToolCallID    string
	ToolName      string
	Minimum       int64
	PairedMinimum int64
}

type entry struct {
	Ordinal     int
	Offset      int64
	Length      int64
	Type        string
	ID          string
	ParentID    string
	TargetID    string
	Role        string
	Segments    []segment
	SubagentIDs []string
	Status      statusData
	Session     indexedSessionData
}

type indexedSessionData struct {
	Role          string
	Timestamp     string
	Text          string
	FinalText     string
	HasFinalText  bool
	MetadataKnown bool
}

type statusData struct {
	Kind          string
	Provider      string
	ModelID       string
	ThinkingLevel string
	Usage         map[string]any
	StopReason    string
	SummaryLength int
	FirstKeptID   string
	EstimateChars int
	EstimateKnown bool
	Excluded      bool
}

type index struct {
	path                     string
	device                   uint64
	inode                    uint64
	size                     int64
	mtime                    time.Time
	entries                  []entry
	byID                     map[string]int
	session                  *Session
	supported                bool
	sessionMetadataSupported bool
	bytes                    int64
}

type cacheItem struct {
	index *index
	used  uint64
}

type Cache struct {
	mu      sync.Mutex
	buildMu sync.Mutex
	clock   uint64
	bytes   int64
	items   map[string]*cacheItem
}

func NewCache() *Cache { return &Cache{items: make(map[string]*cacheItem)} }

func (cache *Cache) Index(path string) (*index, error) {
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	cache.mu.Lock()
	cache.clock++
	device, inode := fileIdentity(stat)
	if item := cache.items[path]; item != nil && item.index.device == device && item.index.inode == inode && item.index.size == stat.Size() && item.index.mtime.Equal(stat.ModTime()) {
		item.used = cache.clock
		result := item.index
		cache.mu.Unlock()
		return result, nil
	}
	cache.mu.Unlock()

	cache.buildMu.Lock()
	defer cache.buildMu.Unlock()
	stat, err = os.Stat(path)
	if err != nil {
		return nil, err
	}
	device, inode = fileIdentity(stat)
	cache.mu.Lock()
	cache.clock++
	if item := cache.items[path]; item != nil && item.index.device == device && item.index.inode == inode && item.index.size == stat.Size() && item.index.mtime.Equal(stat.ModTime()) {
		item.used = cache.clock
		result := item.index
		cache.mu.Unlock()
		return result, nil
	}
	cache.mu.Unlock()
	built, err := buildIndex(path, stat)
	if err != nil {
		return nil, err
	}
	if built.bytes > maxCacheBytes {
		cache.mu.Lock()
		if old := cache.items[path]; old != nil {
			cache.bytes -= old.index.bytes
			delete(cache.items, path)
		}
		cache.mu.Unlock()
		return built, nil
	}
	cache.mu.Lock()
	cache.clock++
	if old := cache.items[path]; old != nil {
		cache.bytes -= old.index.bytes
	}
	cache.items[path] = &cacheItem{index: built, used: cache.clock}
	cache.bytes += built.bytes
	for cache.bytes > maxCacheBytes || len(cache.items) > maxCacheEntries {
		var oldestPath string
		oldest := ^uint64(0)
		for candidatePath, item := range cache.items {
			if candidatePath != path && item.used < oldest {
				oldestPath, oldest = candidatePath, item.used
			}
		}
		if oldestPath == "" {
			break
		}
		cache.bytes -= cache.items[oldestPath].index.bytes
		delete(cache.items, oldestPath)
	}
	cache.mu.Unlock()
	return built, nil
}

type Store struct {
	Root  string
	Home  string
	Cache *Cache
}

func (cache *Cache) cachedIndex(path string) *index {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if item := cache.items[path]; item != nil {
		cache.clock++
		item.used = cache.clock
		return item.index
	}
	return nil
}

func (store Store) Sessions() ([]*Session, error) {
	result, _, err := store.SessionsDeferringMetadata(nil)
	return result, err
}

func (store Store) SessionsDeferringMetadata(deferFor func(string) bool) ([]*Session, bool, error) {
	root, err := filepath.EvalSymlinks(store.Root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var result []*Session
	deferred := false
	err = filepath.WalkDir(root, func(path string, item os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			return nil
		}
		if item.IsDir() || item.Type()&os.ModeSymlink != 0 || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		realPath, err := filepath.EvalSymlinks(path)
		if err != nil || !withinRoot(realPath, root) {
			return nil
		}
		stat, err := os.Stat(realPath)
		if err != nil || !stat.Mode().IsRegular() {
			return nil
		}
		indexed := (*index)(nil)
		if deferFor != nil && deferFor(realPath) {
			indexed = store.Cache.cachedIndex(realPath)
			if indexed != nil {
				deferred = true
			}
		}
		if indexed == nil {
			indexed, err = store.Cache.Index(realPath)
		}
		if err != nil || !indexed.supported || !indexed.sessionMetadataSupported || indexed.session == nil || indexed.session.CWD == "" || !filepath.IsAbs(indexed.session.CWD) {
			return nil
		}
		if stat, err := os.Stat(indexed.session.CWD); err != nil || !stat.IsDir() {
			return nil
		}
		copy := *indexed.session
		result = append(result, &copy)
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	sort.SliceStable(result, func(left, right int) bool {
		return result[left].ConversationActivityAt.After(result[right].ConversationActivityAt)
	})
	return result, deferred, nil
}

func (store Store) Session(path string) (*Session, bool) {
	path = filepath.Clean(path)
	root, err := filepath.EvalSymlinks(store.Root)
	if err != nil {
		return nil, false
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || !withinRoot(realPath, root) || filepath.Ext(realPath) != ".jsonl" {
		return nil, false
	}
	indexed, err := store.Cache.Index(realPath)
	if err != nil || !indexed.supported || !indexed.sessionMetadataSupported || indexed.session == nil || !filepath.IsAbs(indexed.session.CWD) {
		return nil, false
	}
	if stat, err := os.Stat(indexed.session.CWD); err != nil || !stat.IsDir() {
		return nil, false
	}
	copy := *indexed.session
	return &copy, true
}

func (store Store) Window(path, leafID string, leafSupplied bool, cursor *int, after *int) (Window, error) {
	return store.windowWithValidationHook(path, leafID, leafSupplied, cursor, after, nil)
}

func (store Store) windowWithValidationHook(path, leafID string, leafSupplied bool, cursor *int, after *int, beforeValidation func(int)) (Window, error) {
	canonical, ok := store.canonicalSessionPath(path)
	if !ok {
		return Window{}, os.ErrNotExist
	}
	for attempt := 0; attempt < 3; attempt++ {
		window, err := store.windowOnce(canonical, leafID, leafSupplied, cursor, after, func() {
			if beforeValidation != nil {
				beforeValidation(attempt)
			}
		})
		if !errors.Is(err, errSessionChanged) {
			return window, err
		}
	}
	return Window{}, errSessionChanged
}

func (store Store) windowOnce(path, leafID string, leafSupplied bool, cursor *int, after *int, beforeValidation func()) (Window, error) {
	indexed, err := store.Cache.Index(path)
	if err != nil {
		return Window{}, err
	}
	if !indexed.supported {
		return Window{}, errors.New("session contains an entry larger than the bounded index supports")
	}
	effectiveLeaf := leafID
	if !leafSupplied {
		effectiveLeaf = indexed.latestLeafID()
	}
	branch, ok := indexed.entriesForLeaf(effectiveLeaf, true)
	if !ok {
		return Window{}, os.ErrNotExist
	}
	units, projectionOK := projectedUnits(branch)
	if !projectionOK {
		return Window{}, errors.New("session contains mismatched tool call metadata")
	}
	end := len(units)
	if cursor != nil {
		end = clamp(*cursor, 0, len(units))
	}
	messages, start, err := renderWindow(path, indexed, units, end, after, store.Home)
	if err != nil {
		return Window{}, err
	}
	if beforeValidation != nil {
		beforeValidation()
	}
	stat, err := os.Stat(path)
	if err != nil || !indexSnapshotValid(indexed, stat) {
		return Window{}, errSessionChanged
	}
	return Window{
		Messages: messages, StartIndex: start, EndIndex: end, TotalMessageCount: len(units), TreeLeafID: effectiveLeaf,
		LatestStableLeafID: indexed.stableLeafID(indexed.latestLeafID()), CurrentStableLeafID: indexed.stableLeafID(leafID),
		Status: indexed.sessionStatus(),
	}, nil
}

func (store Store) Status(path string) (Status, error) {
	canonical, ok := store.canonicalSessionPath(path)
	if !ok {
		return Status{}, os.ErrNotExist
	}
	path = canonical
	indexed, err := store.Cache.Index(path)
	if err != nil {
		return Status{}, err
	}
	if !indexed.supported {
		return Status{}, errors.New("session contains an unsupported oversized entry")
	}
	return indexed.sessionStatus(), nil
}

func (store Store) Generation(path string) string {
	canonical, ok := store.canonicalSessionPath(path)
	if !ok {
		return ""
	}
	path = canonical
	indexed, err := store.Cache.Index(path)
	if err != nil || indexed.session == nil || indexed.session.ID == "" {
		return ""
	}
	stat, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if system, ok := stat.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d:%s", system.Dev, system.Ino, indexed.session.ID)
	}
	return fmt.Sprintf("%d:%d:%s", stat.Size(), stat.ModTime().UnixNano(), indexed.session.ID)
}

func (store Store) canonicalSessionPath(path string) (string, bool) {
	root, err := filepath.EvalSymlinks(store.Root)
	if err != nil {
		return "", false
	}
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil || !withinRoot(realPath, root) || filepath.Ext(realPath) != ".jsonl" {
		return "", false
	}
	stat, err := os.Stat(realPath)
	return realPath, err == nil && stat.Mode().IsRegular()
}

func buildIndex(path string, _ os.FileInfo) (*index, error) {
	return buildIndexWithValidationHook(path, nil)
}

func buildIndexWithValidationHook(path string, beforeValidation func(int)) (*index, error) {
	for attempt := 0; attempt < 3; attempt++ {
		stat, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		built, err := buildIndexOnce(path, stat, func() {
			if beforeValidation != nil {
				beforeValidation(attempt)
			}
		})
		if !errors.Is(err, errSessionChanged) {
			return built, err
		}
	}
	return nil, errSessionChanged
}

var errSessionChanged = errors.New("session changed while it was indexed")

func buildIndexOnce(path string, stat os.FileInfo, beforeValidation func()) (*index, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	device, inode := fileIdentity(stat)
	result := &index{path: path, device: device, inode: inode, size: stat.Size(), mtime: stat.ModTime(), byID: make(map[string]int), supported: true, sessionMetadataSupported: true, bytes: 256}
	reader := bufio.NewReaderSize(io.LimitReader(file, stat.Size()), 64<<10)
	var offset int64
	for ordinal := 0; ; ordinal++ {
		line, length, largeEntry, readErr := readIndexedLine(reader)
		if length == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if errors.Is(readErr, errEntryTooLarge) || errors.Is(readErr, errEntryOverCap) {
			result.supported = false
			if result.bytes > maxBuiltIndexBytes {
				return nil, errors.New("session index exceeds memory bound")
			}
			offset += length
			continue
		}
		if largeEntry != nil {
			largeEntry.Ordinal = ordinal
			largeEntry.Offset = offset
			largeEntry.Length = length
			result.entries = append(result.entries, *largeEntry)
			if largeEntry.ID != "" {
				result.byID[largeEntry.ID] = len(result.entries) - 1
			}
			result.bytes += estimatedEntryBytes(*largeEntry)
			result.applyIndexedSessionMetadata(*largeEntry)
			if result.bytes > maxBuiltIndexBytes {
				return nil, errors.New("session index exceeds memory bound")
			}
			offset += length
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) != 0 {
			var raw map[string]any
			if json.Unmarshal(trimmed, &raw) == nil {
				indexedEntry := metadataFromRaw(raw, ordinal, offset, length)
				result.entries = append(result.entries, indexedEntry)
				if indexedEntry.ID != "" {
					result.byID[indexedEntry.ID] = len(result.entries) - 1
				}
				result.bytes += estimatedEntryBytes(indexedEntry)
				result.applySessionMetadata(raw, indexedEntry, stat)
			}
		}
		if result.bytes > maxBuiltIndexBytes {
			return nil, errors.New("session index exceeds memory bound")
		}
		offset += length
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	if result.session != nil {
		if result.session.DisplayName == "" {
			result.session.DisplayName = result.session.FirstUserMessage
			if result.session.DisplayName == "" {
				result.session.DisplayName = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			}
		}
		result.bytes += int64(len(result.session.Path) + len(result.session.CWD) + len(result.session.ID) + len(result.session.DisplayName) + len(result.session.FirstUserMessage) + len(result.session.LatestAssistantResponsePreview) + len(result.session.ParentSessionPath))
	}
	if beforeValidation != nil {
		beforeValidation()
	}
	after, err := os.Stat(path)
	if err != nil || !indexSnapshotValid(result, after) {
		return nil, errSessionChanged
	}
	return result, nil
}

func indexSnapshotValid(indexed *index, stat os.FileInfo) bool {
	if stat == nil {
		return false
	}
	device, inode := fileIdentity(stat)
	if device != indexed.device || inode != indexed.inode || stat.Size() < indexed.size {
		return false
	}
	return stat.Size() > indexed.size || stat.ModTime().Equal(indexed.mtime)
}

var (
	errEntryTooLarge = errors.New("unsupported oversized JSONL entry")
	errEntryOverCap  = errors.New("JSONL entry exceeds 64 MiB cap")
)

func readIndexedLine(reader *bufio.Reader) ([]byte, int64, *entry, error) {
	var materialized []byte
	var total int64
	var scanner *indexJSONScanner
	overCap := false
	for {
		part, err := reader.ReadSlice('\n')
		total += int64(len(part))
		if total > MaxRenderedEntryBytes {
			overCap = true
		} else if scanner != nil {
			scanner.feed(part)
		} else if len(materialized)+len(part) <= MaxIndexedEntryBytes {
			materialized = append(materialized, part...)
		} else {
			scanner = newIndexJSONScanner()
			scanner.feed(materialized)
			scanner.feed(part)
			materialized = nil
		}
		if err == nil || errors.Is(err, io.EOF) {
			if overCap {
				return nil, total, nil, errEntryOverCap
			}
			if scanner != nil {
				metadata, ok := scanner.finish()
				if !ok {
					return nil, total, nil, errEntryTooLarge
				}
				return nil, total, &metadata, nil
			}
			return materialized, total, nil, err
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, total, nil, err
		}
	}
}

func estimatedEntryBytes(item entry) int64 {
	value := 176 + len(item.Type) + len(item.ID) + len(item.ParentID) + len(item.TargetID) + len(item.Role)
	for _, segment := range item.Segments {
		value += 64 + len(segment.Role) + len(segment.ToolCallID) + len(segment.ToolName)
	}
	for _, id := range item.SubagentIDs {
		value += 40 + len(id)
	}
	value += len(item.Status.Kind) + len(item.Status.Provider) + len(item.Status.ModelID) + len(item.Status.ThinkingLevel) + len(item.Status.StopReason) + len(item.Status.Usage)*64
	value += len(item.Session.Role) + len(item.Session.Timestamp) + len(item.Session.Text) + len(item.Session.FinalText)
	return int64(value)
}

func metadataFromRaw(raw map[string]any, ordinal int, offset, length int64) entry {
	result := entry{Ordinal: ordinal, Offset: offset, Length: length, Type: stringValue(raw["type"]), ID: stringValue(raw["id"]), ParentID: stringValue(raw["parentId"]), TargetID: stringValue(raw["targetId"])}
	message, _ := raw["message"].(map[string]any)
	result.Role = stringValue(message["role"])
	parsed := messagesFromRaw(raw, "")
	for _, item := range parsed {
		result.Segments = append(result.Segments, segment{Role: item.Role, ToolCallID: item.ToolCallID, ToolName: item.ToolName})
	}
	if result.Role == "assistant" {
		for _, part := range arrayValue(message["content"]) {
			object := asMap(part)
			if stringValue(object["type"]) == "toolCall" && stringValue(object["name"]) == "subagent" && stringValue(object["id"]) != "" {
				result.SubagentIDs = append(result.SubagentIDs, stringValue(object["id"]))
			}
		}
	}
	switch result.Type {
	case "model_change":
		result.Status = statusData{Kind: result.Type, Provider: stringValue(raw["provider"]), ModelID: firstString(raw["modelId"], raw["model"])}
	case "thinking_level_change":
		result.Status = statusData{Kind: result.Type, ThinkingLevel: firstString(raw["thinkingLevel"], raw["thinking_level"])}
	case "message":
		result.Status.EstimateChars, result.Status.EstimateKnown = estimatedMessageCharacters(message)
		if result.Role == "bashExecution" && boolValue(message["excludeFromContext"]) {
			result.Status.Excluded = true
		}
		if result.Role == "assistant" {
			usage, _ := message["usage"].(map[string]any)
			estimate, known, excluded := result.Status.EstimateChars, result.Status.EstimateKnown, result.Status.Excluded
			result.Status = statusData{Kind: "assistant", Provider: stringValue(message["provider"]), ModelID: stringValue(message["model"]), Usage: indexedUsage(usage), StopReason: stringValue(message["stopReason"]), EstimateChars: estimate, EstimateKnown: known, Excluded: excluded}
		}
	case "compaction":
		result.Status = statusData{Kind: "compaction", SummaryLength: utf8.RuneCountInString(stringValue(raw["summary"])), FirstKeptID: stringValue(raw["firstKeptEntryId"])}
	}
	return result
}

func estimatedMessageCharacters(message map[string]any) (int, bool) {
	if stringValue(message["role"]) == "bashExecution" {
		command, commandOK := message["command"].(string)
		output, outputOK := message["output"].(string)
		if !commandOK || !outputOK {
			return 0, false
		}
		var exitCode *int
		if number, ok := message["exitCode"].(float64); ok && number == math.Trunc(number) {
			value := int(number)
			exitCode = &value
		}
		var fullPathCharacters *int
		if fullPath := stringValue(message["fullOutputPath"]); fullPath != "" {
			value := utf8.RuneCountInString(fullPath)
			fullPathCharacters = &value
		}
		return bashExecutionCharacterLength(
			utf8.RuneCountInString(command), utf8.RuneCountInString(output), output == "", exitCode,
			boolValue(message["cancelled"]), boolValue(message["truncated"]), fullPathCharacters,
		), true
	}
	values := 0
	count := 0
	for _, part := range arrayValue(message["content"]) {
		switch value := part.(type) {
		case string:
			values += utf8.RuneCountInString(value)
			count++
		case map[string]any:
			typeName := stringValue(value["type"])
			if typeName == "image" {
				continue
			}
			var text string
			switch typeName {
			case "text":
				text = stringValue(value["text"])
			case "thinking":
				text = stripThinkingHeading(stringValue(value["thinking"]))
			default:
				return 0, false
			}
			values += utf8.RuneCountInString(text)
			count++
		}
	}
	if count > 1 {
		values += count - 1
	}
	return values, true
}

func bashExecutionCharacterLength(commandCharacters, outputCharacters int, outputEmpty bool, exitCode *int, cancelled, truncated bool, fullPathCharacters *int) int {
	length := len("Ran ``\n") + commandCharacters
	if outputEmpty {
		length += len("(no output)")
	} else {
		length += len("```\n\n```") + outputCharacters
	}
	if cancelled {
		length += len("\n\n(command cancelled)")
	} else if exitCode != nil && *exitCode != 0 {
		length += len("\n\nCommand exited with code ") + len(strconv.Itoa(*exitCode))
	}
	if truncated && fullPathCharacters != nil {
		length += len("\n\n[Output truncated. Full output: ]") + *fullPathCharacters
	}
	return length
}

func indexedUsage(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	result := make(map[string]any)
	for _, key := range []string{"totalTokens", "total_tokens", "tokens", "contextWindow", "context_window", "contextLimit", "context_limit", "contextPercent", "context_percent", "costTotal", "cost_total"} {
		if value, exists := source[key]; exists {
			result[key] = value
		}
	}
	if costs, ok := source["cost"].(map[string]any); ok {
		if total, exists := costs["total"]; exists {
			result["cost"] = map[string]any{"total": total}
		}
	}
	return result
}

func (indexed *index) applyIndexedSessionMetadata(item entry) {
	if indexed.session == nil || item.Type != "message" {
		return
	}
	if !item.Session.MetadataKnown {
		indexed.sessionMetadataSupported = false
		return
	}
	role := item.Session.Role
	if role != "toolResult" && role != "bashExecution" {
		indexed.session.MessageCount++
	}
	when := parseTime(item.Session.Timestamp)
	if role == "user" {
		if indexed.session.FirstUserMessage == "" {
			indexed.session.FirstUserMessage = item.Session.Text
		}
		if when.After(indexed.session.ConversationActivityAt) {
			indexed.session.ConversationActivityAt = when
		}
	}
	if role == "assistant" && item.Session.HasFinalText {
		indexed.session.AssistantResponseCount++
		indexed.session.LatestAssistantResponsePreview = preview(item.Session.FinalText)
		if item.Status.StopReason == "" || item.Status.StopReason == "stop" || item.Status.StopReason == "length" {
			if when.After(indexed.session.ConversationActivityAt) {
				indexed.session.ConversationActivityAt = when
			}
		}
	}
}

func (indexed *index) applySessionMetadata(raw map[string]any, item entry, stat os.FileInfo) {
	if item.Type == "session" && indexed.session == nil {
		created := parseTime(stringValue(raw["timestamp"]))
		if created.IsZero() {
			created = stat.ModTime()
		}
		indexed.session = &Session{Path: indexed.path, CWD: stringValue(raw["cwd"]), ID: stringValue(raw["id"]), ParentSessionPath: stringValue(raw["parentSession"]), CreatedAt: created, ModifiedAt: stat.ModTime(), ConversationActivityAt: created}
	}
	if indexed.session == nil {
		return
	}
	if item.Type == "session_info" {
		indexed.session.DisplayName = strings.TrimSpace(stringValue(raw["name"]))
		return
	}
	if item.Type != "message" {
		return
	}
	message, _ := raw["message"].(map[string]any)
	role := stringValue(message["role"])
	if role != "toolResult" && role != "bashExecution" {
		indexed.session.MessageCount++
	}
	text := contentText(message["content"])
	when := parseTime(stringValue(raw["timestamp"]))
	if role == "user" {
		if indexed.session.FirstUserMessage == "" {
			indexed.session.FirstUserMessage = text
		}
		if when.After(indexed.session.ConversationActivityAt) {
			indexed.session.ConversationActivityAt = when
		}
	}
	if role == "assistant" {
		answer := finalAssistantText(message["content"])
		if strings.TrimSpace(answer) != "" {
			indexed.session.AssistantResponseCount++
			indexed.session.LatestAssistantResponsePreview = preview(answer)
			if stop := stringValue(message["stopReason"]); stop == "" || stop == "stop" || stop == "length" {
				if when.After(indexed.session.ConversationActivityAt) {
					indexed.session.ConversationActivityAt = when
				}
			}
		}
	}
	if indexed.session.DisplayName == "" {
		indexed.session.DisplayName = indexed.session.FirstUserMessage
		if indexed.session.DisplayName == "" {
			indexed.session.DisplayName = strings.TrimSuffix(filepath.Base(indexed.path), filepath.Ext(indexed.path))
		}
	}
}

func (indexed *index) latestLeafID() string {
	leaf := ""
	for _, item := range indexed.entries {
		if item.ID == "" || item.Type == "session" {
			continue
		}
		if item.Type == "leaf" {
			leaf = item.TargetID
		} else {
			leaf = item.ID
		}
	}
	return leaf
}

func (indexed *index) entriesForLeaf(leaf string, supplied bool) ([]entry, bool) {
	if !supplied {
		return indexed.entries, true
	}
	if leaf == "" {
		var result []entry
		for _, item := range indexed.entries {
			if item.Type == "session" || item.ID == "" {
				result = append(result, item)
			}
		}
		return result, true
	}
	path := make(map[string]bool)
	current := leaf
	for current != "" && !path[current] {
		position, ok := indexed.byID[current]
		if !ok {
			break
		}
		item := indexed.entries[position]
		path[current] = true
		current = item.ParentID
	}
	if len(path) == 0 {
		return nil, false
	}
	var result []entry
	for _, item := range indexed.entries {
		if item.ID == "" || path[item.ID] {
			result = append(result, item)
		}
	}
	return result, true
}

func (indexed *index) stableLeafID(leaf string) string {
	seen := make(map[string]bool)
	for leaf != "" && !seen[leaf] {
		position, ok := indexed.byID[leaf]
		if !ok {
			break
		}
		seen[leaf] = true
		item := indexed.entries[position]
		if item.Type != "custom_message" && item.Role != "user" {
			break
		}
		leaf = item.ParentID
	}
	return leaf
}

func (indexed *index) sessionStatus() Status {
	var result Status
	latestUsageOrdinal := -1
	latestCompactionOrdinal := -1
	for ordinal, item := range indexed.entries {
		data := item.Status
		switch data.Kind {
		case "model_change":
			if data.Provider != "" {
				result.Provider = data.Provider
			}
			if data.ModelID != "" {
				result.ModelID = data.ModelID
			}
		case "thinking_level_change":
			if data.ThinkingLevel != "" {
				result.ThinkingLevel = data.ThinkingLevel
			}
		case "assistant":
			if data.Provider != "" {
				result.Provider = data.Provider
			}
			if data.ModelID != "" {
				result.ModelID = data.ModelID
			}
			if data.StopReason == "aborted" || data.StopReason == "error" || data.Usage == nil {
				continue
			}
			if tokens, ok := numberFrom(data.Usage, "totalTokens", "total_tokens", "tokens"); ok && tokens > 0 {
				latestUsageOrdinal = ordinal
				result.ContextTokens, result.HasContextTokens = tokens, true
				result.ContextPercent, result.CostTotal = 0, 0
				if limit, ok := numberFrom(data.Usage, "contextWindow", "context_window", "contextLimit", "context_limit"); ok {
					result.ContextLimit, result.HasContextLimit = limit, true
				} else {
					result.HasContextLimit = false
				}
				if percent, ok := numberFrom(data.Usage, "contextPercent", "context_percent"); ok {
					result.ContextPercent = percent
				}
				if cost, ok := numberFrom(data.Usage, "costTotal", "cost_total"); ok {
					result.CostTotal = cost
				} else if costs, ok := data.Usage["cost"].(map[string]any); ok {
					result.CostTotal, _ = numberFrom(costs, "total")
				}
			}
		case "compaction":
			latestCompactionOrdinal = ordinal
		}
	}
	if latestCompactionOrdinal > latestUsageOrdinal {
		indexed.applyCompactionEstimate(&result, latestCompactionOrdinal)
	}
	return result
}

func (indexed *index) applyCompactionEstimate(status *Status, ordinal int) {
	compaction := indexed.entries[ordinal]
	start := ordinal
	if position, ok := indexed.byID[compaction.Status.FirstKeptID]; ok {
		start = position
	}
	characters, count := compaction.Status.SummaryLength, 1
	for position := start; position < ordinal; position++ {
		item := indexed.entries[position]
		if item.Type != "message" || item.Status.Excluded {
			continue
		}
		if !item.Status.EstimateKnown {
			status.HasContextTokens = false
			status.HasContextLimit = false
			status.ContextEstimated = false
			return
		}
		characters += item.Status.EstimateChars
		count++
	}
	if count > 1 {
		characters += count - 1
	}
	if characters <= 0 {
		return
	}
	status.ContextTokens = math.Ceil(float64(characters) / 4)
	status.HasContextTokens = true
	status.HasContextLimit = false
	status.ContextPercent = 0
	status.ContextEstimated = true
}

type unit struct {
	Key          [2]int
	Entry        entry
	Dependencies []entry
	Estimate     int64
}

func projectedUnits(entries []entry) ([]unit, bool) {
	pending := make(map[string]int)
	subagentSources := make(map[string]entry)
	for _, item := range entries {
		for _, id := range item.SubagentIDs {
			if _, exists := subagentSources[id]; !exists {
				subagentSources[id] = item
			}
		}
	}
	var result []unit
	for _, item := range entries {
		for segmentIndex, part := range item.Segments {
			if part.Role == "toolResult" {
				if position, ok := pending[part.ToolCallID]; ok {
					if result[position].Entry.Segments[result[position].Key[1]].ToolName != part.ToolName {
						return nil, false
					}
					result[position].Dependencies = append(result[position].Dependencies, item)
					result[position].Estimate = max(result[position].Estimate, part.PairedMinimum)
					delete(pending, part.ToolCallID)
					continue
				}
			}
			created := unit{Key: [2]int{item.Ordinal, segmentIndex}, Entry: item, Estimate: part.Minimum}
			if part.Role == "toolResult" && part.ToolName == "subagent" {
				if source, ok := subagentSources[part.ToolCallID]; ok {
					created.Dependencies = append(created.Dependencies, source)
				}
			}
			result = append(result, created)
			if part.ToolCallID != "" && pairToolResult(part.ToolName) {
				pending[part.ToolCallID] = len(result) - 1
			}
		}
	}
	return result, true
}

func renderWindow(path string, indexed *index, units []unit, end int, after *int, home string) ([]*Message, int, error) {
	start := 0
	forward := after != nil
	if forward {
		start = clamp(*after, 0, end)
	}
	position := end - 1
	step := -1
	if forward {
		position = start
		step = 1
	}
	var messages []*Message
	var bytesUsed int64
	var retainedBytes int64
	hasUser := false
	for position < end && ((!forward && position >= 0) || (forward && position >= start)) && len(messages) < WindowMaxMessages {
		item := units[position]
		enough := hasUser || len(messages) >= WindowMinMessages
		if enough && bytesUsed+item.Estimate > WindowByteBudget {
			break
		}
		rendered, err := renderUnits(path, indexed, []unit{item}, home)
		if err != nil {
			return nil, 0, err
		}
		if len(rendered) != 1 {
			return nil, 0, errors.New("indexed session projection changed while it was rendered")
		}
		messageBytes := renderedMessageBytes(rendered[0])
		if enough && bytesUsed+messageBytes > WindowByteBudget {
			break
		}
		messageRetainedBytes := retainedMessageBytes(rendered[0])
		if messageRetainedBytes > MaxRetainedWindowBytes {
			return nil, 0, errors.New("rendered session message exceeds memory bound")
		}
		if retainedBytes+messageRetainedBytes > MaxRetainedWindowBytes {
			break
		}
		messages = append(messages, rendered[0])
		bytesUsed += messageBytes
		retainedBytes += messageRetainedBytes
		hasUser = hasUser || rendered[0].Role == "user"
		if !forward {
			start = position
		}
		position += step
	}
	if !forward {
		slices.Reverse(messages)
	}
	return messages, start, nil
}

func retainedMessageBytes(message *Message) int64 {
	value := len(message.Role) + len(message.Text) + len(message.Summary) + len(message.ToolCallID) + len(message.ToolName) +
		len(message.ToolSummaryHTML) + len(message.ToolPrompt) + len(message.EntryID) + len(message.CustomType) + len(message.BashFullOutputPath)
	for _, image := range message.Images {
		value += len(image.Data) + len(image.MIMEType) + len(image.Src)
	}
	return int64(value)
}

func renderedMessageBytes(message *Message) int64 {
	value := int64((len(message.Role) + len(message.Text) + len(message.Summary)) * 2)
	for _, image := range message.Images {
		value += int64(len(image.Data))
	}
	return value
}

func renderUnits(path string, indexed *index, selected []unit, home string) ([]*Message, error) {
	if len(selected) == 0 {
		return nil, nil
	}
	needed := make(map[int]entry)
	selectedKeys := make(map[[2]int]bool)
	for _, item := range selected {
		needed[item.Entry.Ordinal] = item.Entry
		selectedKeys[item.Key] = true
		for _, dependency := range item.Dependencies {
			needed[dependency.Ordinal] = dependency
		}
	}
	ordinals := make([]int, 0, len(needed))
	for ordinal := range needed {
		ordinals = append(ordinals, ordinal)
	}
	sort.Ints(ordinals)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var messages []*Message
	type subagentContext struct {
		prompt    string
		timestamp time.Time
	}
	subagents := make(map[string]subagentContext)
	for _, ordinal := range ordinals {
		item := needed[ordinal]
		if item.Length > MaxRenderedEntryBytes {
			return nil, errors.New("selected session entry exceeds rendering bound")
		}
		data := make([]byte, item.Length)
		if _, err := file.ReadAt(data, item.Offset); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		var raw map[string]any
		if err := json.Unmarshal(bytes.TrimSpace(data), &raw); err != nil {
			return nil, err
		}
		if message := asMap(raw["message"]); stringValue(message["role"]) == "assistant" {
			for _, part := range arrayValue(message["content"]) {
				call := asMap(part)
				if stringValue(call["type"]) == "toolCall" && stringValue(call["name"]) == "subagent" && stringValue(call["id"]) != "" {
					id := stringValue(call["id"])
					if _, exists := subagents[id]; !exists {
						subagents[id] = subagentContext{prompt: subagentPrompt(call["arguments"]), timestamp: parseTime(stringValue(raw["timestamp"]))}
					}
				}
			}
		}
		parsed := messagesFromRaw(raw, home)
		for segmentIndex, message := range parsed {
			message.Key = [2]int{item.Ordinal, segmentIndex}
			messages = append(messages, message)
		}
	}
	for _, message := range messages {
		if message.Role == "toolResult" && message.ToolName == "subagent" {
			if context, ok := subagents[message.ToolCallID]; ok {
				if context.prompt != "" {
					message.ToolPrompt = context.prompt
				}
				if !context.timestamp.IsZero() {
					message.Timestamp = context.timestamp
				}
			}
		}
	}
	messages = pairMessages(messages)
	result := messages[:0]
	for _, message := range messages {
		if selectedKeys[message.Key] {
			result = append(result, message)
		}
	}
	return result, nil
}

func messagesFromRaw(raw map[string]any, home string) []*Message {
	typeName := stringValue(raw["type"])
	when := parseTime(stringValue(raw["timestamp"]))
	if typeName == "compaction" {
		text := strings.TrimSpace(stringValue(raw["summary"]))
		if text == "" {
			encoded, _ := json.MarshalIndent(raw, "", "  ")
			text = string(encoded)
		}
		return []*Message{{Role: "status", Text: text, Timestamp: when, Compact: true, Summary: "Conversation compacted", Compaction: true}}
	}
	if typeName == "custom_message" {
		if displayed, _ := raw["display"].(bool); !displayed {
			return nil
		}
		return []*Message{{Role: "custom", Text: contentText(raw["content"]), Timestamp: when, EntryID: stringValue(raw["id"]), Images: contentImages(raw["content"]), CustomType: stringValue(raw["customType"])}}
	}
	if typeName == "error" || raw["error"] != nil || raw["finalError"] != nil {
		if text := errorText(raw); text != "" {
			return []*Message{{Role: "error", Text: text, Timestamp: when, Error: true}}
		}
	}
	if typeName != "message" {
		return nil
	}
	message, _ := raw["message"].(map[string]any)
	role := stringValue(message["role"])
	if role == "" {
		return nil
	}
	if role == "bashExecution" && validBashExecution(message) {
		command := DisplayHomePath(stringValue(message["command"]), home)
		var exitCode *int
		if number, ok := message["exitCode"].(float64); ok {
			value := int(number)
			exitCode = &value
		}
		return []*Message{{Role: role, Text: stringValue(message["output"]), Timestamp: when, EntryID: stringValue(raw["id"]), Compact: true, Summary: "$ " + command, Error: exitCode != nil && *exitCode != 0, ToolName: "bash", ToolTranscript: true, BashExitCode: exitCode, BashCancelled: boolValue(message["cancelled"]), BashTruncated: boolValue(message["truncated"]), BashExcludedFromContext: boolValue(message["excludeFromContext"]), BashFullOutputPath: DisplayHomePath(stringValue(message["fullOutputPath"]), home), BashRecordedAt: time.UnixMilli(int64(numberValue(message["timestamp"])))}}
	}
	if role != "assistant" {
		generalSubagent := isGeneralSubagent(message)
		text := contentText(message["content"])
		if generalSubagent {
			text = generalSubagentText(message)
		}
		if role == "user" {
			text = skillCommandDisplayText(text)
		}
		if role == "toolResult" && stringValue(message["toolName"]) == "edit" {
			if details, ok := message["details"].(map[string]any); ok && stringValue(details["diff"]) != "" {
				text = stringValue(details["diff"])
			}
		}
		images := contentImages(message["content"])
		if text == "" && len(images) == 0 {
			return nil
		}
		toolName := stringValue(message["toolName"])
		summary := firstString(message["toolName"], "tool result")
		if generalSubagent {
			summary = "subagent general"
		}
		return []*Message{{Role: role, Text: text, Timestamp: when, EntryID: stringValue(raw["id"]), Compact: role == "toolResult", Summary: summary, Error: boolValue(message["isError"]), ToolCallID: stringValue(message["toolCallId"]), ToolName: toolName, Images: images, ToolTranscript: generalSubagent || transcriptTool(toolName), ToolPrompt: subagentPrompt(message["details"])}}
	}
	return assistantMessages(message, when, home)
}

func assistantMessages(message map[string]any, when time.Time, home string) []*Message {
	parts := arrayValue(message["content"])
	type group struct {
		compact bool
		parts   []any
	}
	var groups []group
	for _, part := range parts {
		object, _ := part.(map[string]any)
		if stringValue(object["type"]) == "toolCall" && stringValue(object["name"]) == "subagent" {
			continue
		}
		thinking := stringValue(object["type"]) == "thinking"
		compact := stringValue(object["type"]) == "toolCall" || stringValue(object["type"]) == "toolResult"
		previousThinking := len(groups) > 0 && stringValue(asMap(groups[len(groups)-1].parts[0])["type"]) == "thinking"
		if thinking || compact || len(groups) == 0 || groups[len(groups)-1].compact || previousThinking {
			groups = append(groups, group{compact: compact, parts: []any{part}})
		} else {
			groups[len(groups)-1].parts = append(groups[len(groups)-1].parts, part)
		}
	}
	var result []*Message
	for _, group := range groups {
		text := contentText(group.parts)
		if text == "" && !group.compact {
			continue
		}
		var toolCall map[string]any
		for _, part := range group.parts {
			if object, ok := part.(map[string]any); ok && stringValue(object["type"]) == "toolCall" {
				toolCall = object
				break
			}
		}
		toolName := stringValue(toolCall["name"])
		thinking := len(group.parts) == 1 && stringValue(asMap(group.parts[0])["type"]) == "thinking"
		summary := ""
		toolHTML := ""
		toolPreview := false
		if group.compact {
			summary = compactSummary(group.parts, home)
			if transcriptTool(toolName) {
				arguments := asMap(toolCall["arguments"])
				path := DisplayHomePath(stringValue(arguments["path"]), home)
				toolHTML = `<span class="tool-command">` + escapeHTML(toolName) + `</span>`
				if path != "" {
					toolHTML += ` <span class="tool-path">` + escapeHTML(path) + `</span>`
				}
				if offset, ok := integer(arguments["offset"]); ok {
					if limit, ok := integer(arguments["limit"]); ok {
						toolHTML += `<span class="tool-range">:` + strconv.Itoa(offset) + `-` + strconv.Itoa(offset+limit-1) + `</span>`
					}
				}
			}
			toolPreview = toolName == "edit"
		}
		result = append(result, &Message{Role: "assistant", Text: text, Timestamp: when, Compact: group.compact, Summary: summary, ToolCallID: stringValue(toolCall["id"]), ToolName: toolName, Thinking: thinking, ToolSummaryHTML: toolHTML, ToolTranscript: transcriptTool(toolName), ToolPreview: toolPreview, FinalAssistantResponse: finalAssistantText(group.parts) != ""})
	}
	return result
}

func pairMessages(messages []*Message) []*Message {
	pending := make(map[string]*Message)
	result := make([]*Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == "toolResult" {
			if call := pending[message.ToolCallID]; call != nil {
				delete(pending, message.ToolCallID)
				switch call.ToolName {
				case "bash":
					call.Text = message.Text
				case "read":
					if message.Error {
						call.Text = strings.TrimSpace(call.Text + "\n\n" + message.Text)
					} else {
						call.Text = ""
					}
				case "write":
					call.Text = strings.TrimSpace(call.Text + "\n\n" + message.Text)
				case "edit":
					if message.Error {
						call.Text = strings.TrimSpace(call.Text + "\n\n" + message.Text)
					} else {
						call.Text = message.Text
					}
				default:
					call.Text = strings.TrimSpace(call.Text + "\n\n" + message.Text)
				}
				call.Error = call.Error || message.Error
				if !message.Error {
					call.ToolPreview = false
				}
				call.Images = append(call.Images, message.Images...)
				continue
			}
		}
		result = append(result, message)
		if message.ToolCallID != "" && pairToolResult(message.ToolName) {
			pending[message.ToolCallID] = message
		}
	}
	return result
}

func contentText(content any) string {
	var values []string
	for _, part := range arrayValue(content) {
		switch value := part.(type) {
		case string:
			values = append(values, value)
		case map[string]any:
			typeName := stringValue(value["type"])
			if text, ok := value["text"].(string); ok {
				values = append(values, text)
				continue
			}
			if typeName == "thinking" {
				values = append(values, stripThinkingHeading(stringValue(value["thinking"])))
				continue
			}
			if typeName == "toolResult" {
				values = append(values, firstString(value["output"], value["result"], "[tool result]"))
				continue
			}
			if typeName == "toolCall" {
				name := firstString(value["name"], "tool")
				if name == "bash" || name == "read" {
					continue
				}
				if name == "write" {
					values = append(values, previewText("+", stringValue(asMap(value["arguments"])["content"])))
					continue
				}
				if name == "edit" {
					values = append(values, editPreview(asMap(value["arguments"])["edits"]))
					continue
				}
				arguments, _ := json.MarshalIndent(value["arguments"], "", "  ")
				if len(arguments) > 0 && string(arguments) != "null" && string(arguments) != "{}" {
					values = append(values, "[tool: "+name+"]\n"+string(arguments))
				} else {
					values = append(values, "[tool: "+name+"]")
				}
			}
		}
	}
	return strings.Join(values, "\n")
}

func contentImages(content any) []Image {
	var result []Image
	for _, part := range arrayValue(content) {
		object, _ := part.(map[string]any)
		mimeType, data := stringValue(object["mimeType"]), stringValue(object["data"])
		if stringValue(object["type"]) == "image" && imageMIMETypes[mimeType] && data != "" {
			result = append(result, Image{Data: data, MIMEType: mimeType})
		}
	}
	return result
}

func finalAssistantText(content any) string {
	var values []string
	for _, part := range arrayValue(content) {
		if text, ok := part.(string); ok {
			values = append(values, text)
			continue
		}
		object, _ := part.(map[string]any)
		if stringValue(object["type"]) != "text" {
			continue
		}
		if assistantTextPhase(stringValue(object["textSignature"])) == "commentary" {
			continue
		}
		values = append(values, stringValue(object["text"]))
	}
	return strings.TrimSpace(strings.Join(values, "\n"))
}

func compactSummary(parts []any, home string) string {
	var labels []string
	for _, part := range parts {
		object := asMap(part)
		switch stringValue(object["type"]) {
		case "thinking":
			if stringValue(object["thinking"]) != "" {
				labels = append(labels, "thinking")
			}
		case "toolCall":
			name := firstString(object["name"], "tool")
			if name == "bash" {
				args := asMap(object["arguments"])
				suffix := ""
				if args["timeout"] != nil {
					suffix = " (timeout " + fmt.Sprint(args["timeout"]) + "s)"
				}
				labels = append(labels, "$ "+DisplayHomePath(stringValue(args["command"]), home)+suffix)
			} else {
				labels = append(labels, name)
			}
		case "toolResult":
			labels = append(labels, firstString(object["toolName"], "tool"))
		}
	}
	return strings.Join(unique(labels), " + ")
}

func previewText(prefix, text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) > 6 {
		lines = append(lines[:6], "…")
	}
	for index := range lines {
		lines[index] = prefix + " " + lines[index]
	}
	return strings.Join(lines, "\n")
}

func editPreview(value any) string {
	var groups []string
	for index, candidate := range arrayValue(value) {
		edit := asMap(candidate)
		groups = append(groups, fmt.Sprintf("Edit %d\n%s\n%s", index+1, previewText("-", stringValue(edit["oldText"])), previewText("+", stringValue(edit["newText"]))))
	}
	return strings.Join(groups, "\n\n")
}

func stripThinkingHeading(text string) string {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if strings.HasPrefix(trimmed, "**") {
		if end := strings.Index(trimmed[2:], "**"); end >= 0 {
			rest := trimmed[end+4:]
			if strings.HasPrefix(rest, "\n\n") {
				return strings.TrimPrefix(rest, "\n\n")
			}
		}
	}
	return text
}

func validBashExecution(message map[string]any) bool {
	_, command := message["command"].(string)
	_, output := message["output"].(string)
	_, timestamp := message["timestamp"].(float64)
	_, cancelled := message["cancelled"].(bool)
	_, truncated := message["truncated"].(bool)
	if !command || !output || !timestamp || !cancelled || !truncated {
		return false
	}
	if exitCode, exists := message["exitCode"]; exists && exitCode != nil {
		if number, ok := exitCode.(float64); !ok || number != math.Trunc(number) {
			return false
		}
	}
	if path, exists := message["fullOutputPath"]; exists {
		if _, ok := path.(string); !ok {
			return false
		}
	}
	if excluded, exists := message["excludeFromContext"]; exists {
		if _, ok := excluded.(bool); !ok {
			return false
		}
	}
	return true
}

func errorText(value any) string {
	switch item := value.(type) {
	case string:
		return strings.TrimSpace(item)
	case map[string]any:
		for _, key := range []string{"error", "finalError", "message", "text"} {
			if text := errorText(item[key]); text != "" {
				return text
			}
		}
		if details, ok := item["details"].(map[string]any); ok {
			return errorText(details)
		}
	}
	return ""
}

var expandedSkillPrompt = regexp.MustCompile(`(?s)^<skill name="([^"\n]+)" location="([^"\n]+)">\nReferences are relative to ([^\n]+)\.\n\n.*\n</skill>(?:\n\n(.*))?$`)

func skillCommandDisplayText(text string) string {
	match := expandedSkillPrompt.FindStringSubmatch(text)
	if match == nil || !filepath.IsAbs(match[2]) || filepath.Clean(match[2]) != match[2] || filepath.Dir(match[2]) != match[3] {
		return text
	}
	command := "/skill:" + match[1]
	if match[4] != "" {
		command += " " + match[4]
	}
	return command
}

func assistantTextPhase(signature string) string {
	if !strings.HasPrefix(signature, "{") {
		return ""
	}
	var parsed map[string]any
	if json.Unmarshal([]byte(signature), &parsed) != nil || parsed["v"] != float64(1) {
		return ""
	}
	if _, ok := parsed["id"].(string); !ok {
		return ""
	}
	phase := stringValue(parsed["phase"])
	if phase == "commentary" || phase == "final_answer" {
		return phase
	}
	return ""
}

func isGeneralSubagent(message map[string]any) bool {
	details := asMap(message["details"])
	_, tools := details["tools"].([]any)
	_, usage := details["usage"].(map[string]any)
	return stringValue(message["toolName"]) == "subagent" && tools && usage
}
func generalSubagentText(message map[string]any) string {
	details := asMap(message["details"])
	statusIcon := func(value string) string {
		if value == "done" {
			return "✓"
		}
		if value == "error" {
			return "✗"
		}
		return "⏳"
	}
	lines := []string{statusIcon(stringValue(details["status"])) + " general"}
	for _, candidate := range arrayValue(details["tools"]) {
		tool := asMap(candidate)
		name := stringValue(tool["name"])
		arguments := asMap(tool["args"])
		description := name
		path := firstString(arguments["path"], arguments["file_path"])
		switch name {
		case "bash":
			description = "$ " + firstString(arguments["command"], "...")
		case "read":
			description = "read " + firstString(path, "...")
			offset, offsetOK := flexibleInteger(arguments["offset"])
			limit, limitOK := flexibleInteger(arguments["limit"])
			if offsetOK || limitOK {
				if !offsetOK {
					offset = 1
				}
				description += ":" + strconv.Itoa(offset)
				if limitOK {
					description += "-" + strconv.Itoa(offset+limit-1)
				}
			}
		case "write", "edit":
			description = name + " " + firstString(path, "...")
		case "grep":
			description = "grep /" + stringValue(arguments["pattern"]) + "/ in " + firstString(arguments["path"], ".")
		case "find":
			description = "find " + firstString(arguments["pattern"], "*") + " in " + firstString(arguments["path"], ".")
		case "ls":
			description = "ls " + firstString(arguments["path"], ".")
		default:
			encoded, err := json.Marshal(arguments)
			if err == nil {
				serialized := string(encoded)
				characters := []rune(serialized)
				if len(characters) > 100 {
					serialized = string(characters[:100]) + "…"
				}
				description = firstString(name, "tool") + " " + serialized
			}
		}
		lines = append(lines, statusIcon(stringValue(tool["status"]))+" "+description)
		for _, line := range strings.Split(strings.TrimSpace(stringValue(tool["output"])), "\n") {
			if line != "" {
				lines = append(lines, "  "+line)
			}
		}
	}
	final := stringValue(details["streamingText"])
	if final == "" {
		items := arrayValue(details["textItems"])
		if len(items) > 0 {
			final = stringValue(items[len(items)-1])
		}
	}
	if final == "" {
		final = contentText(message["content"])
	}
	if final != "" {
		lines = append(lines, "", final)
	}
	if usage := generalSubagentUsageText(asMap(details["usage"]), stringValue(details["model"])); usage != "" {
		lines = append(lines, "", usage)
	}
	return strings.Join(lines, "\n")
}

func generalSubagentUsageText(usage map[string]any, model string) string {
	var parts []string
	if count := int(numericUsageValue(usage["turns"])); count > 0 {
		label := "turns"
		if count == 1 {
			label = "turn"
		}
		parts = append(parts, strconv.Itoa(count)+" "+label)
	}
	for _, item := range []struct {
		key    string
		prefix string
	}{{"input", "↑"}, {"output", "↓"}, {"cacheRead", "R"}, {"cacheWrite", "W"}} {
		if value := numericUsageValue(usage[item.key]); value > 0 {
			parts = append(parts, item.prefix+compactUsageNumber(value))
		}
	}
	if cost := numericUsageValue(usage["cost"]); cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", cost))
	}
	if context := numericUsageValue(usage["contextTokens"]); context > 0 {
		parts = append(parts, "ctx:"+compactUsageNumber(context))
	}
	if model != "" {
		parts = append(parts, model)
	}
	return strings.Join(parts, " ")
}

func numericUsageValue(value any) float64 {
	var number float64
	switch candidate := value.(type) {
	case float64:
		number = candidate
	case string:
		number, _ = strconv.ParseFloat(candidate, 64)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return 0
	}
	return number
}

func compactUsageNumber(value float64) string {
	switch {
	case value < 1000:
		return strconv.FormatFloat(math.Round(value), 'f', 0, 64)
	case value < 10_000:
		return fmt.Sprintf("%.1fk", value/1000)
	case value < 1_000_000:
		return strconv.FormatFloat(math.Round(value/1000), 'f', 0, 64) + "k"
	default:
		return fmt.Sprintf("%.1fM", value/1_000_000)
	}
}

func flexibleInteger(value any) (int, bool) {
	switch candidate := value.(type) {
	case float64:
		if candidate == math.Trunc(candidate) {
			return int(candidate), true
		}
	case string:
		parsed, err := strconv.Atoi(candidate)
		return parsed, err == nil
	}
	return 0, false
}

func subagentPrompt(value any) string {
	object := asMap(value)
	if task := stringValue(object["task"]); task != "" {
		return task
	}
	for _, key := range []string{"tasks", "chain", "results"} {
		var values []string
		for _, candidate := range arrayValue(object[key]) {
			item := asMap(candidate)
			if task := stringValue(item["task"]); task != "" {
				if agent := stringValue(item["agent"]); agent != "" {
					task = agent + ": " + task
				}
				values = append(values, task)
			}
		}
		if len(values) > 0 {
			return strings.Join(values, "\n\n")
		}
	}
	return ""
}

func SessionHash(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])
}
func MessageHash(text string) string {
	normalized := strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"))
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}
func fileIdentity(stat os.FileInfo) (uint64, uint64) {
	if system, ok := stat.Sys().(*syscall.Stat_t); ok {
		return uint64(system.Dev), uint64(system.Ino)
	}
	return 0, 0
}
func withinRoot(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}
func parseTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
func preview(value string) string {
	value = strings.Join(strings.Fields(strings.ReplaceAll(value, "javascript:", "")), " ")
	if len([]rune(value)) > 180 {
		chars := []rune(value)
		return string(chars[:177]) + "…"
	}
	return value
}
func stringValue(value any) string { result, _ := value.(string); return result }
func firstString(values ...any) string {
	for _, value := range values {
		if result := stringValue(value); result != "" {
			return result
		}
	}
	return ""
}
func boolValue(value any) bool      { result, _ := value.(bool); return result }
func numberValue(value any) float64 { result, _ := value.(float64); return result }
func asMap(value any) map[string]any {
	result, _ := value.(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}
func arrayValue(value any) []any {
	if result, ok := value.([]any); ok {
		return result
	}
	if value == nil {
		return nil
	}
	return []any{value}
}
func integer(value any) (int, bool) {
	number, ok := value.(float64)
	return int(number), ok && number == float64(int(number))
}
func numberFrom(values map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := values[key].(float64); ok {
			return value, true
		}
	}
	return 0, false
}
func pairToolResult(name string) bool {
	return name == "bash" || name == "read" || name == "edit" || name == "write"
}
func transcriptTool(name string) bool { return name == "read" || name == "edit" || name == "write" }
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
func unique(values []string) []string {
	seen := make(map[string]bool)
	result := values[:0]
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}
func escapeHTML(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return replacer.Replace(value)
}
