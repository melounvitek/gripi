package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const attachmentMatchWindow = 5 * time.Minute

type Attachment struct {
	MessageHash string   `json:"message_hash"`
	Timestamp   string   `json:"timestamp"`
	Count       int      `json:"count"`
	Paths       []string `json:"paths"`
	MIMETypes   []string `json:"mime_types"`
}

type AttachmentMatch struct {
	Count  int
	Images []Image
}

type AttachmentStore struct {
	Root         string
	SessionsRoot string
}

func (store AttachmentStore) RecordPrompt(sessionPath, message string, imageCount int, timestamp time.Time, paths, mimeTypes []string) error {
	if imageCount == 0 {
		return nil
	}
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(store.Root, SessionHash(sessionPath)+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	unlock, err := lockAttachmentFile(file)
	if err != nil {
		return err
	}
	defer unlock()
	record := Attachment{MessageHash: MessageHash(message), Timestamp: timestamp.UTC().Format("2006-01-02T15:04:05.000000Z"), Count: imageCount, Paths: paths, MIMETypes: mimeTypes}
	encoded, err := json.Marshal(record)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(file, "%s\n", encoded)
	return err
}

func (store AttachmentStore) Migrate(fromSessionPath, toSessionPath string) (func() error, error) {
	if fromSessionPath == toSessionPath {
		return nil, nil
	}
	fromPath := filepath.Join(store.Root, SessionHash(fromSessionPath)+".jsonl")
	from, err := os.ReadFile(fromPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(store.Root, 0700); err != nil {
		return nil, err
	}
	toPath := filepath.Join(store.Root, SessionHash(toSessionPath)+".jsonl")
	existing, err := os.ReadFile(toPath)
	existed := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	migrated, rollbackImages, err := store.migrateAttachmentImages(fromSessionPath, toSessionPath, from)
	if err != nil {
		return nil, err
	}
	if len(migrated) > 0 && bytes.HasSuffix(existing, migrated) {
		return nil, nil
	}
	if err := replaceFile(toPath, append(existing, migrated...)); err != nil {
		_ = rollbackImages()
		return nil, err
	}
	rollback := func() error {
		var metadataErr error
		if !existed {
			metadataErr = os.Remove(toPath)
			if errors.Is(metadataErr, os.ErrNotExist) {
				metadataErr = nil
			}
		} else {
			metadataErr = replaceFile(toPath, existing)
		}
		return errors.Join(metadataErr, rollbackImages())
	}
	return rollback, nil
}

func (store AttachmentStore) migrateAttachmentImages(fromSessionPath, toSessionPath string, metadata []byte) ([]byte, func() error, error) {
	fromDirectory := filepath.Join(store.Root, SessionHash(fromSessionPath))
	toDirectory := filepath.Join(store.Root, SessionHash(toSessionPath))
	created := []string{}
	rollback := func() error {
		var first error
		for _, path := range created {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
				first = err
			}
		}
		_ = os.Remove(toDirectory)
		return first
	}
	var migrated bytes.Buffer
	for _, line := range bytes.Split(metadata, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var attachment Attachment
		if json.Unmarshal(line, &attachment) != nil {
			migrated.Write(line)
			migrated.WriteByte('\n')
			continue
		}
		for index, source := range attachment.Paths {
			relative, err := filepath.Rel(fromDirectory, source)
			if err != nil || relative == ".." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				continue
			}
			destination := filepath.Join(toDirectory, relative)
			if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
				if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
					return nil, func() error { return nil }, errors.Join(err, rollback())
				}
				if err := copyAttachmentImage(source, destination); err != nil {
					return nil, func() error { return nil }, errors.Join(err, rollback())
				}
				created = append(created, destination)
			} else if err != nil {
				return nil, func() error { return nil }, errors.Join(err, rollback())
			}
			attachment.Paths[index] = destination
		}
		encoded, err := json.Marshal(attachment)
		if err != nil {
			return nil, func() error { return nil }, errors.Join(err, rollback())
		}
		migrated.Write(encoded)
		migrated.WriteByte('\n')
	}
	return migrated.Bytes(), rollback, nil
}

func copyAttachmentImage(sourcePath, destinationPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(destinationPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	succeeded := false
	defer func() {
		_ = destination.Close()
		if !succeeded {
			_ = os.Remove(destinationPath)
		}
	}()
	buffer := make([]byte, 32<<10)
	if _, err := io.CopyBuffer(destination, source, buffer); err != nil {
		return err
	}
	if err := destination.Sync(); err != nil {
		return err
	}
	if err := destination.Close(); err != nil {
		return err
	}
	succeeded = true
	return nil
}

func replaceFile(path string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".attachment-migration-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(contents)
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func (store AttachmentStore) Match(sessionPath string, messages []*Message) map[*Message]AttachmentMatch {
	attachments := store.read(sessionPath)
	matches := make(map[*Message]AttachmentMatch)
	used := make(map[int]bool)
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		hash := MessageHash(message.Text)
		best := -1
		bestDistance := attachmentMatchWindow + time.Nanosecond
		for index, attachment := range attachments {
			if used[index] || attachment.MessageHash != hash {
				continue
			}
			when, _ := time.Parse(time.RFC3339Nano, attachment.Timestamp)
			if message.Timestamp.IsZero() || when.IsZero() {
				if best < 0 {
					best = index
				}
				continue
			}
			distance := message.Timestamp.Sub(when)
			if distance < 0 {
				distance = -distance
			}
			if distance <= attachmentMatchWindow && distance < bestDistance {
				best, bestDistance = index, distance
			}
		}
		if best < 0 {
			continue
		}
		used[best] = true
		attachment := attachments[best]
		match := AttachmentMatch{Count: attachment.Count}
		for index, path := range attachment.Paths {
			mimeType := ""
			if index < len(attachment.MIMETypes) {
				mimeType = attachment.MIMETypes[index]
			}
			relative, err := filepath.Rel(store.Root, path)
			if err != nil || relative == ".." || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
				continue
			}
			match.Images = append(match.Images, Image{Src: "/attachments/" + filepath.ToSlash(relative), MIMEType: mimeType})
		}
		matches[message] = match
	}
	return matches
}

func (store AttachmentStore) read(sessionPath string) []Attachment {
	paths := []string{sessionPath}
	if store.SessionsRoot != "" {
		if aliases := SessionPathAliases(store.SessionsRoot, sessionPath); len(aliases) > 0 {
			paths = aliases
		}
	}
	var result []Attachment
	seenInEarlierFiles := make(map[string]bool)
	for _, path := range paths {
		file, err := os.Open(filepath.Join(store.Root, SessionHash(path)+".jsonl"))
		if err != nil {
			continue
		}
		seenInThisFile := make(map[string]bool)
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		for scanner.Scan() {
			encoded := scanner.Text()
			if seenInEarlierFiles[encoded] {
				continue
			}
			var attachment Attachment
			if json.Unmarshal([]byte(encoded), &attachment) == nil {
				result = append(result, attachment)
				seenInThisFile[encoded] = true
			}
		}
		_ = file.Close()
		for encoded := range seenInThisFile {
			seenInEarlierFiles[encoded] = true
		}
	}
	return result
}
