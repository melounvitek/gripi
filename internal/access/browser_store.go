package access

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/melounvitek/gripi/internal/state"
)

const (
	PruneInterval        = 24 * time.Hour
	UnrequestedRetention = 7 * 24 * time.Hour
	DeniedRetention      = 7 * 24 * time.Hour
	RequestedRetention   = 30 * 24 * time.Hour
	MaxPendingRequests   = 100
	MaxTerminalRequests  = 100
	MaxIPBytes           = 64
	MaxUserAgentBytes    = 512
)

type Status string

const (
	StatusApproved Status = "approved"
	StatusDenied   Status = "denied"
	StatusPending  Status = "pending"
	StatusCreated  Status = "created"
)

type ApprovedBrowser struct {
	Token      string `json:"token"`
	ApprovedAt string `json:"approved_at"`
	Label      string `json:"label"`
}

type PendingRequest struct {
	Code        string  `json:"code"`
	Token       string  `json:"token"`
	Requested   bool    `json:"requested"`
	CreatedAt   string  `json:"created_at"`
	RequestedAt *string `json:"requested_at"`
	IP          string  `json:"ip"`
	UserAgent   string  `json:"user_agent"`
	DeniedAt    *string `json:"denied_at,omitempty"`
}

type PendingRequestsFullError struct {
	Limit int
}

func (e *PendingRequestsFullError) Error() string {
	return fmt.Sprintf("Pending browser request limit reached (%d)", e.Limit)
}

type BrowserStore struct {
	file         *state.File
	mu           sync.Mutex
	lastPrunedAt time.Time
	now          func() time.Time
}

type browserState struct {
	ApprovedBrowsers []ApprovedBrowser `json:"approved_browsers"`
	PendingRequests  []PendingRequest  `json:"pending_requests"`
}

func NewBrowserStore(path string) *BrowserStore {
	return &BrowserStore{file: state.NewFile(path), now: time.Now}
}

func (s *BrowserStore) Approved(token string) (bool, error) {
	if token == "" {
		return false, nil
	}
	if err := s.pruneIfDue(); err != nil {
		return false, err
	}
	current, err := s.data()
	if err != nil {
		return false, err
	}
	for _, browser := range current.ApprovedBrowsers {
		if browser.Token == token {
			return true, nil
		}
	}
	return false, nil
}

func (s *BrowserStore) EnsurePending(token, ip, userAgent string) (PendingRequest, error) {
	createdAt := formatTime(s.now())
	var result PendingRequest
	err := s.update(func(current *browserState) error {
		for _, request := range current.PendingRequests {
			if request.Token == token {
				result = request
				return nil
			}
		}
		if err := enforcePendingLimit(current, -1); err != nil {
			return err
		}
		code, err := uniqueCode(current)
		if err != nil {
			return err
		}
		result = PendingRequest{
			Code: code, Token: token, CreatedAt: createdAt,
			IP: boundedString(ip, MaxIPBytes), UserAgent: boundedString(userAgent, MaxUserAgentBytes),
		}
		current.PendingRequests = append(current.PendingRequests, result)
		return nil
	})
	return result, err
}

func (s *BrowserStore) RequestAccess(token, ip, userAgent string) (PendingRequest, error) {
	now := formatTime(s.now())
	var result PendingRequest
	err := s.update(func(current *browserState) error {
		index := pendingRequestIndex(current.PendingRequests, token)
		if index < 0 {
			if err := enforcePendingLimit(current, -1); err != nil {
				return err
			}
			code, err := uniqueCode(current)
			if err != nil {
				return err
			}
			current.PendingRequests = append(current.PendingRequests, PendingRequest{
				Code: code, Token: token, CreatedAt: now,
				IP: boundedString(ip, MaxIPBytes), UserAgent: boundedString(userAgent, MaxUserAgentBytes),
			})
			index = len(current.PendingRequests) - 1
		}
		request := &current.PendingRequests[index]
		if request.DeniedAt != nil {
			if err := enforcePendingLimit(current, index); err != nil {
				return err
			}
		}
		request.DeniedAt = nil
		request.Requested = true
		request.RequestedAt = stringPointer(now)
		result = *request
		return nil
	})
	return result, err
}

func (s *BrowserStore) PendingRequest(token string) (PendingRequest, bool, error) {
	current, err := s.data()
	if err != nil {
		return PendingRequest{}, false, err
	}
	for _, request := range current.PendingRequests {
		if request.Token == token && request.DeniedAt == nil {
			return request, true, nil
		}
	}
	return PendingRequest{}, false, nil
}

func (s *BrowserStore) PendingRequests() ([]PendingRequest, error) {
	current, err := s.data()
	if err != nil {
		return nil, err
	}
	requests := make([]PendingRequest, 0, len(current.PendingRequests))
	for _, request := range current.PendingRequests {
		if request.Requested && request.DeniedAt == nil {
			requests = append(requests, request)
		}
	}
	return requests, nil
}

func (s *BrowserStore) ApproveCode(code string) (PendingRequest, bool, error) {
	return s.approveRequest(func(request PendingRequest) bool { return request.Code == code })
}

func (s *BrowserStore) ApproveToken(token string) (PendingRequest, bool, error) {
	return s.approveRequest(func(request PendingRequest) bool { return request.Token == token })
}

func (s *BrowserStore) ApproveCurrentBrowser(token, label string) (bool, error) {
	if token == "" {
		return false, nil
	}
	err := s.update(func(current *browserState) error {
		s.addApprovedBrowser(current, token, label)
		current.PendingRequests = deletePendingTokens(current.PendingRequests, token)
		return nil
	})
	return err == nil, err
}

func (s *BrowserStore) ReplaceBrowserToken(oldToken, newToken, label string) (bool, error) {
	if newToken == "" {
		return false, nil
	}
	err := s.update(func(current *browserState) error {
		browsers := current.ApprovedBrowsers[:0]
		for _, browser := range current.ApprovedBrowsers {
			if browser.Token != oldToken {
				browsers = append(browsers, browser)
			}
		}
		current.ApprovedBrowsers = browsers
		current.PendingRequests = deletePendingTokens(current.PendingRequests, oldToken, newToken)
		s.addApprovedBrowser(current, newToken, label)
		return nil
	})
	return err == nil, err
}

func (s *BrowserStore) DenyCode(code string) (PendingRequest, bool, error) {
	var result PendingRequest
	found := false
	err := s.update(func(current *browserState) error {
		for index := range current.PendingRequests {
			request := &current.PendingRequests[index]
			if request.Code == code {
				request.DeniedAt = stringPointer(formatTime(s.now()))
				result = *request
				found = true
				break
			}
		}
		return nil
	})
	return result, found, err
}

func (s *BrowserStore) PendingStatus(token string) (Status, error) {
	if token != "" {
		if err := s.pruneIfDue(); err != nil {
			return "", err
		}
	}
	current, err := s.data()
	if err != nil {
		return "", err
	}
	for _, browser := range current.ApprovedBrowsers {
		if browser.Token == token && token != "" {
			return StatusApproved, nil
		}
	}
	for _, request := range current.PendingRequests {
		if request.Token != token {
			continue
		}
		if request.DeniedAt != nil {
			return StatusDenied, nil
		}
		if request.Requested {
			return StatusPending, nil
		}
		break
	}
	return StatusCreated, nil
}

func (s *BrowserStore) approveRequest(matches func(PendingRequest) bool) (PendingRequest, bool, error) {
	var result PendingRequest
	found := false
	err := s.update(func(current *browserState) error {
		for index, request := range current.PendingRequests {
			if !matches(request) {
				continue
			}
			result = request
			found = true
			s.addApprovedBrowser(current, request.Token, request.UserAgent)
			current.PendingRequests = append(current.PendingRequests[:index], current.PendingRequests[index+1:]...)
			break
		}
		return nil
	})
	return result, found, err
}

func (s *BrowserStore) addApprovedBrowser(current *browserState, token, label string) {
	for _, browser := range current.ApprovedBrowsers {
		if browser.Token == token {
			return
		}
	}
	current.ApprovedBrowsers = append(current.ApprovedBrowsers, ApprovedBrowser{
		Token: token, ApprovedAt: formatTime(s.now()), Label: boundedString(label, MaxUserAgentBytes),
	})
}

func (s *BrowserStore) data() (browserState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readState()
}

func (s *BrowserStore) update(change func(*browserState) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.readState()
	if err != nil {
		return err
	}
	now := s.now()
	prunePendingRequests(&current, now)
	if err := change(&current); err != nil {
		return err
	}
	prunePendingRequests(&current, s.now())
	pruneTerminalRequests(&current)
	if err := s.writeState(current); err != nil {
		return err
	}
	s.lastPrunedAt = s.now()
	return nil
}

func (s *BrowserStore) pruneIfDue() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if now.Sub(s.lastPrunedAt) < PruneInterval {
		return nil
	}
	current, err := s.readState()
	if err != nil {
		return err
	}
	changed := prunePendingRequests(&current, now)
	if pruneTerminalRequests(&current) {
		changed = true
	}
	if changed {
		if err := s.writeState(current); err != nil {
			return err
		}
	}
	s.lastPrunedAt = now
	return nil
}

func (s *BrowserStore) readState() (browserState, error) {
	contents, exists, err := s.file.Read()
	if err != nil {
		return browserState{}, err
	}
	if !exists {
		return emptyBrowserState(), nil
	}
	var current browserState
	if err := json.Unmarshal(contents, &current); err != nil {
		return browserState{}, fmt.Errorf("parse browser access state: %w", err)
	}
	if current.ApprovedBrowsers == nil {
		current.ApprovedBrowsers = []ApprovedBrowser{}
	}
	if current.PendingRequests == nil {
		current.PendingRequests = []PendingRequest{}
	}
	for index := range current.PendingRequests {
		current.PendingRequests[index].IP = boundedString(current.PendingRequests[index].IP, MaxIPBytes)
		current.PendingRequests[index].UserAgent = boundedString(current.PendingRequests[index].UserAgent, MaxUserAgentBytes)
	}
	return current, nil
}

func (s *BrowserStore) writeState(current browserState) error {
	contents, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return err
	}
	contents = append(contents, '\n')
	return s.file.Write(contents)
}

func emptyBrowserState() browserState {
	return browserState{ApprovedBrowsers: []ApprovedBrowser{}, PendingRequests: []PendingRequest{}}
}

func enforcePendingLimit(current *browserState, excluded int) error {
	active := 0
	for index, request := range current.PendingRequests {
		if index != excluded && request.DeniedAt == nil {
			active++
		}
	}
	if active >= MaxPendingRequests {
		return &PendingRequestsFullError{Limit: MaxPendingRequests}
	}
	return nil
}

func pendingRequestIndex(requests []PendingRequest, token string) int {
	for index, request := range requests {
		if request.Token == token {
			return index
		}
	}
	return -1
}

func deletePendingTokens(requests []PendingRequest, tokens ...string) []PendingRequest {
	kept := requests[:0]
	for _, request := range requests {
		remove := false
		for _, token := range tokens {
			if request.Token == token {
				remove = true
				break
			}
		}
		if !remove {
			kept = append(kept, request)
		}
	}
	return kept
}

func prunePendingRequests(current *browserState, now time.Time) bool {
	kept := current.PendingRequests[:0]
	for _, request := range current.PendingRequests {
		if !stalePendingRequest(request, now) {
			kept = append(kept, request)
		}
	}
	changed := len(kept) != len(current.PendingRequests)
	current.PendingRequests = kept
	return changed
}

func pruneTerminalRequests(current *browserState) bool {
	type terminalRequest struct {
		index int
		at    time.Time
	}
	var terminal []terminalRequest
	for index, request := range current.PendingRequests {
		if request.DeniedAt != nil {
			at, _ := parseTime(*request.DeniedAt)
			terminal = append(terminal, terminalRequest{index: index, at: at})
		}
	}
	overflow := len(terminal) - MaxTerminalRequests
	if overflow <= 0 {
		return false
	}
	sort.SliceStable(terminal, func(i, j int) bool { return terminal[i].at.Before(terminal[j].at) })
	remove := make(map[int]struct{}, overflow)
	for _, request := range terminal[:overflow] {
		remove[request.index] = struct{}{}
	}
	kept := current.PendingRequests[:0]
	for index, request := range current.PendingRequests {
		if _, found := remove[index]; !found {
			kept = append(kept, request)
		}
	}
	current.PendingRequests = kept
	return true
}

func stalePendingRequest(request PendingRequest, now time.Time) bool {
	timestamp := request.CreatedAt
	retention := UnrequestedRetention
	if request.DeniedAt != nil {
		timestamp = *request.DeniedAt
		retention = DeniedRetention
	} else if request.Requested {
		if request.RequestedAt != nil {
			timestamp = *request.RequestedAt
		}
		retention = RequestedRetention
	}
	parsed, ok := parseTime(timestamp)
	return !ok || now.Sub(parsed) > retention
}

func parseTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return parsed, err == nil
}

func boundedString(value string, maximum int) string {
	if len(value) > maximum {
		value = value[:maximum]
	}
	if utf8.ValidString(value) {
		return value
	}
	return strings.ToValidUTF8(value, "")
}

func uniqueCode(current *browserState) (string, error) {
	for {
		code, err := randomCode()
		if err != nil {
			return "", err
		}
		unique := true
		for _, request := range current.PendingRequests {
			if request.Code == code {
				unique = false
				break
			}
		}
		if unique {
			return code, nil
		}
	}
}

func randomCode() (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	code := make([]byte, 9)
	for index := range code {
		if index == 4 {
			code[index] = '-'
			continue
		}
		for {
			var value [1]byte
			if _, err := rand.Read(value[:]); err != nil {
				return "", err
			}
			if value[0] < 252 {
				code[index] = alphabet[int(value[0])%len(alphabet)]
				break
			}
		}
	}
	return string(code), nil
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func stringPointer(value string) *string {
	return &value
}
