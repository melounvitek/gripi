package access

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/melounvitek/gripi/internal/state"
)

var ErrSessionOwnedByAnotherWorkspace = errors.New("session is already owned by another workspace")

const (
	workspacePendingRetention = 30 * 24 * time.Hour
	maxPendingWorkspaces      = 100
	maxTerminalWorkspaces     = 100
)

type WorkspacePendingRequestsFullError struct{ Limit int }

func (err *WorkspacePendingRequestsFullError) Error() string {
	return fmt.Sprintf("Pending workspace request limit reached (%d)", err.Limit)
}

type WorkspaceApproval struct {
	WorkspaceID string `json:"workspace_id"`
	ApprovedAt  string `json:"approved_at"`
}

type WorkspaceRequest struct {
	Code         string `json:"code"`
	WorkspaceID  string `json:"workspace_id"`
	BrowserToken string `json:"browser_token"`
	CreatedAt    string `json:"created_at"`
	RequestedAt  string `json:"requested_at"`
	DeniedAt     string `json:"denied_at,omitempty"`
	ApprovedAt   string `json:"approved_at,omitempty"`
}

type workspaceAccessState struct {
	ApprovedWorkspaces []WorkspaceApproval `json:"approved_workspaces"`
	PendingRequests    []WorkspaceRequest  `json:"pending_requests"`
}

type WorkspaceStore struct {
	file  *state.File
	clock func() time.Time
	mu    sync.Mutex
}

func NewWorkspaceStore(path string) *WorkspaceStore {
	return &WorkspaceStore{file: state.NewFile(path), clock: time.Now}
}

func (store *WorkspaceStore) Approved(workspaceID string) (bool, error) {
	if workspaceID == "" {
		return false, nil
	}
	value, err := store.data()
	if err != nil {
		return false, err
	}
	for _, workspace := range value.ApprovedWorkspaces {
		if workspace.WorkspaceID == workspaceID {
			return true, nil
		}
	}
	return false, nil
}

func (store *WorkspaceStore) AnyApproved() (bool, error) {
	value, err := store.data()
	return len(value.ApprovedWorkspaces) > 0, err
}

func (store *WorkspaceStore) RequestForCode(code string) (WorkspaceRequest, bool, error) {
	if code == "" {
		return WorkspaceRequest{}, false, nil
	}
	value, err := store.data()
	if err != nil {
		return WorkspaceRequest{}, false, err
	}
	for _, request := range value.PendingRequests {
		if request.Code == code {
			return request, true, nil
		}
	}
	return WorkspaceRequest{}, false, nil
}

func (store *WorkspaceStore) ApproveWorkspace(workspaceID string) error {
	if workspaceID == "" {
		return nil
	}
	_, err := store.update(func(value *workspaceAccessState) (any, error) {
		store.addApproval(value, workspaceID)
		value.PendingRequests = removeWorkspaceRequests(value.PendingRequests, workspaceID)
		return nil, nil
	})
	return err
}

func (store *WorkspaceStore) RequestAccess(workspaceID, browserToken string) (WorkspaceRequest, error) {
	if workspaceID == "" {
		return WorkspaceRequest{}, nil
	}
	result, err := store.update(func(value *workspaceAccessState) (any, error) {
		now := store.clock().UTC().Format(time.RFC3339)
		index := -1
		for candidate := range value.PendingRequests {
			request := value.PendingRequests[candidate]
			if request.WorkspaceID == workspaceID && request.BrowserToken == browserToken {
				index = candidate
				break
			}
		}
		if index < 0 {
			if activeWorkspaceRequests(value.PendingRequests, -1) >= maxPendingWorkspaces {
				return nil, &WorkspacePendingRequestsFullError{Limit: maxPendingWorkspaces}
			}
			code, err := uniqueWorkspaceCode(value.PendingRequests)
			if err != nil {
				return nil, err
			}
			request := WorkspaceRequest{Code: code, WorkspaceID: workspaceID, BrowserToken: browserToken, CreatedAt: now, RequestedAt: now}
			value.PendingRequests = append(value.PendingRequests, request)
			index = len(value.PendingRequests) - 1
		}
		request := &value.PendingRequests[index]
		if (request.DeniedAt != "" || request.ApprovedAt != "") && activeWorkspaceRequests(value.PendingRequests, index) >= maxPendingWorkspaces {
			return nil, &WorkspacePendingRequestsFullError{Limit: maxPendingWorkspaces}
		}
		request.DeniedAt, request.ApprovedAt, request.RequestedAt = "", "", now
		return *request, nil
	})
	if err != nil {
		return WorkspaceRequest{}, err
	}
	return result.(WorkspaceRequest), nil
}

func (store *WorkspaceStore) PendingRequests() ([]WorkspaceRequest, error) {
	value, err := store.data()
	if err != nil {
		return nil, err
	}
	requests := make([]WorkspaceRequest, 0, len(value.PendingRequests))
	for _, request := range value.PendingRequests {
		if request.DeniedAt == "" && request.ApprovedAt == "" {
			requests = append(requests, request)
		}
	}
	return requests, nil
}

func (store *WorkspaceStore) ApproveCode(code string) (WorkspaceRequest, bool, error) {
	return store.resolveCode(code, true)
}

func (store *WorkspaceStore) DenyCode(code string) (WorkspaceRequest, bool, error) {
	return store.resolveCode(code, false)
}

func (store *WorkspaceStore) resolveCode(code string, approve bool) (WorkspaceRequest, bool, error) {
	result, err := store.update(func(value *workspaceAccessState) (any, error) {
		for index := range value.PendingRequests {
			request := &value.PendingRequests[index]
			if request.Code != code {
				continue
			}
			if approve {
				store.addApproval(value, request.WorkspaceID)
				request.ApprovedAt = store.clock().UTC().Format(time.RFC3339)
			} else {
				request.DeniedAt = store.clock().UTC().Format(time.RFC3339)
			}
			return *request, nil
		}
		return nil, nil
	})
	if err != nil || result == nil {
		return WorkspaceRequest{}, false, err
	}
	return result.(WorkspaceRequest), true, nil
}

func (store *WorkspaceStore) addApproval(value *workspaceAccessState, workspaceID string) {
	for _, workspace := range value.ApprovedWorkspaces {
		if workspace.WorkspaceID == workspaceID {
			return
		}
	}
	value.ApprovedWorkspaces = append(value.ApprovedWorkspaces, WorkspaceApproval{WorkspaceID: workspaceID, ApprovedAt: store.clock().UTC().Format(time.RFC3339)})
}

func (store *WorkspaceStore) data() (workspaceAccessState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, err := store.read()
	if err != nil {
		return workspaceAccessState{}, err
	}
	changed := store.prune(&value)
	if changed {
		err = store.write(value)
	}
	return value, err
}

func (store *WorkspaceStore) update(change func(*workspaceAccessState) (any, error)) (any, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, err := store.read()
	if err != nil {
		return nil, err
	}
	store.prune(&value)
	result, err := change(&value)
	if err != nil {
		return nil, err
	}
	store.prune(&value)
	if err := store.write(value); err != nil {
		return nil, err
	}
	return result, nil
}

func (store *WorkspaceStore) read() (workspaceAccessState, error) {
	contents, found, err := store.file.Read()
	if err != nil || !found {
		return emptyWorkspaceAccessState(), err
	}
	var value workspaceAccessState
	if err := json.Unmarshal(contents, &value); err != nil {
		return workspaceAccessState{}, fmt.Errorf("parse workspace access state: %w", err)
	}
	if value.ApprovedWorkspaces == nil {
		value.ApprovedWorkspaces = []WorkspaceApproval{}
	}
	if value.PendingRequests == nil {
		value.PendingRequests = []WorkspaceRequest{}
	}
	return value, nil
}

func (store *WorkspaceStore) write(value workspaceAccessState) error {
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return store.file.Write(append(contents, '\n'))
}

func (store *WorkspaceStore) prune(value *workspaceAccessState) bool {
	now := store.clock()
	retained := value.PendingRequests[:0]
	changed := false
	for _, request := range value.PendingRequests {
		timestamp := firstNonemptyString(request.ApprovedAt, request.DeniedAt, request.RequestedAt, request.CreatedAt)
		parsed, err := time.Parse(time.RFC3339, timestamp)
		if err != nil || now.Sub(parsed) > workspacePendingRetention {
			changed = true
			continue
		}
		retained = append(retained, request)
	}
	value.PendingRequests = retained
	var terminal []int
	for index, request := range value.PendingRequests {
		if request.DeniedAt != "" || request.ApprovedAt != "" {
			terminal = append(terminal, index)
		}
	}
	if len(terminal) <= maxTerminalWorkspaces {
		return changed
	}
	sort.SliceStable(terminal, func(left, right int) bool {
		return terminalTime(value.PendingRequests[terminal[left]]).Before(terminalTime(value.PendingRequests[terminal[right]]))
	})
	remove := make(map[int]bool)
	for _, index := range terminal[:len(terminal)-maxTerminalWorkspaces] {
		remove[index] = true
	}
	retained = value.PendingRequests[:0]
	for index, request := range value.PendingRequests {
		if !remove[index] {
			retained = append(retained, request)
		}
	}
	value.PendingRequests = retained
	return true
}

func emptyWorkspaceAccessState() workspaceAccessState {
	return workspaceAccessState{ApprovedWorkspaces: []WorkspaceApproval{}, PendingRequests: []WorkspaceRequest{}}
}

func activeWorkspaceRequests(requests []WorkspaceRequest, excluding int) int {
	count := 0
	for index, request := range requests {
		if index != excluding && request.DeniedAt == "" && request.ApprovedAt == "" {
			count++
		}
	}
	return count
}

func removeWorkspaceRequests(requests []WorkspaceRequest, workspaceID string) []WorkspaceRequest {
	result := requests[:0]
	for _, request := range requests {
		if request.WorkspaceID != workspaceID {
			result = append(result, request)
		}
	}
	return result
}

func uniqueWorkspaceCode(requests []WorkspaceRequest) (string, error) {
	for {
		value := make([]byte, 8)
		if _, err := rand.Read(value); err != nil {
			return "", err
		}
		const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		for index := range value {
			value[index] = alphabet[int(value[index])%len(alphabet)]
		}
		code := string(value[:4]) + "-" + string(value[4:])
		found := false
		for _, request := range requests {
			found = found || request.Code == code
		}
		if !found {
			return code, nil
		}
	}
}

func terminalTime(request WorkspaceRequest) time.Time {
	value, err := time.Parse(time.RFC3339, firstNonemptyString(request.ApprovedAt, request.DeniedAt))
	if err != nil {
		return time.Unix(0, 0)
	}
	return value
}

func firstNonemptyString(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

type WorkspaceSecretStore struct{ file *state.File }

func NewWorkspaceSecretStore(path string) *WorkspaceSecretStore {
	return &WorkspaceSecretStore{file: state.NewFile(path)}
}

func (store *WorkspaceSecretStore) Secret() (string, error) {
	contents, found, err := store.file.Read()
	if err != nil {
		return "", err
	}
	if found && strings.TrimSpace(string(contents)) != "" {
		return strings.TrimSpace(string(contents)), nil
	}
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	generated := hex.EncodeToString(value)
	created, err := store.file.CreateOnce([]byte(generated + "\n"))
	if err != nil {
		return "", err
	}
	if created {
		return generated, nil
	}
	contents, _, err = store.file.Read()
	secret := strings.TrimSpace(string(contents))
	if err != nil {
		return "", err
	}
	if secret == "" {
		return "", errors.New("workspace secret file is empty")
	}
	return secret, nil
}

func WorkspaceID(secret, key string) string {
	digest := hmac.New(sha256.New, []byte(secret))
	_, _ = digest.Write([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(digest.Sum(nil))
}

type workspaceOwnershipState struct {
	Sessions map[string]string `json:"sessions"`
}

type WorkspaceOwnershipStore struct {
	file *state.File
	mu   sync.Mutex
}

func NewWorkspaceOwnershipStore(path string) *WorkspaceOwnershipStore {
	return &WorkspaceOwnershipStore{file: state.NewFile(path)}
}

func (store *WorkspaceOwnershipStore) Claim(sessionPath, workspaceID string) (bool, error) {
	if sessionPath == "" || workspaceID == "" {
		return false, nil
	}
	canonical, err := filepath.Abs(sessionPath)
	if err != nil {
		return false, err
	}
	created := false
	err = store.update(func(value *workspaceOwnershipState) error {
		owner := value.Sessions[canonical]
		if owner != "" && owner != workspaceID {
			return ErrSessionOwnedByAnotherWorkspace
		}
		if owner == "" {
			value.Sessions[canonical] = workspaceID
			created = true
		}
		return nil
	})
	return created, err
}

func (store *WorkspaceOwnershipStore) Release(sessionPath, workspaceID string) error {
	canonical, err := filepath.Abs(sessionPath)
	if err != nil {
		return err
	}
	return store.update(func(value *workspaceOwnershipState) error {
		if value.Sessions[canonical] == workspaceID {
			delete(value.Sessions, canonical)
		}
		return nil
	})
}

func (store *WorkspaceOwnershipStore) OwnedBy(sessionPath, workspaceID string) (bool, error) {
	if sessionPath == "" || workspaceID == "" {
		return false, nil
	}
	canonical, err := filepath.Abs(sessionPath)
	if err != nil {
		return false, err
	}
	value, err := store.data()
	return value.Sessions[canonical] == workspaceID, err
}

func (store *WorkspaceOwnershipStore) OwnedPaths(workspaceID string) (map[string]bool, error) {
	value, err := store.data()
	if err != nil {
		return nil, err
	}
	paths := make(map[string]bool)
	for path, owner := range value.Sessions {
		if owner == workspaceID {
			paths[path] = true
		}
	}
	return paths, nil
}

func (store *WorkspaceOwnershipStore) OwnsHash(hash, workspaceID string) (bool, error) {
	if hash == "" || workspaceID == "" {
		return false, nil
	}
	value, err := store.data()
	if err != nil {
		return false, err
	}
	for path, owner := range value.Sessions {
		digest := sha256.Sum256([]byte(path))
		if owner == workspaceID && hex.EncodeToString(digest[:]) == hash {
			return true, nil
		}
	}
	return false, nil
}

func (store *WorkspaceOwnershipStore) data() (workspaceOwnershipState, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.read()
}

func (store *WorkspaceOwnershipStore) update(change func(*workspaceOwnershipState) error) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	value, err := store.read()
	if err != nil {
		return err
	}
	if err := change(&value); err != nil {
		return err
	}
	contents, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return store.file.Write(append(contents, '\n'))
}

func (store *WorkspaceOwnershipStore) read() (workspaceOwnershipState, error) {
	contents, found, err := store.file.Read()
	if err != nil || !found {
		return workspaceOwnershipState{Sessions: map[string]string{}}, err
	}
	var value workspaceOwnershipState
	if err := json.Unmarshal(contents, &value); err != nil {
		return workspaceOwnershipState{}, fmt.Errorf("parse workspace ownership state: %w", err)
	}
	if value.Sessions == nil {
		return workspaceOwnershipState{}, errors.New("workspace ownership state is missing sessions")
	}
	return value, nil
}
