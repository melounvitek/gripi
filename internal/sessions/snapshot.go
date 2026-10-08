package sessions

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// FileSnapshot is saved to session-baselines.json. A baseline written before a field
// changed its name or meaning would make every session look changed behind the
// gateway's back, so such a change needs a new file name.
type FileSnapshot struct {
	Device          uint64
	Inode           uint64
	Size            int64
	MTimeNS         int64
	AppendCursor    string
	PersistedLeafID string
	Complete        bool
}

func (snapshot FileSnapshot) Revision() string {
	complete := 0
	if snapshot.Complete {
		complete = 1
	}
	return fmt.Sprintf("%d:%d:%d:%d:%s:%s:%d", snapshot.Device, snapshot.Inode, snapshot.Size, snapshot.MTimeNS, snapshot.AppendCursor, snapshot.PersistedLeafID, complete)
}

func (store Store) FileSnapshot(path string) (FileSnapshot, error) {
	for attempt := 0; attempt < 3; attempt++ {
		file, err := os.Open(path)
		if err != nil {
			return FileSnapshot{}, err
		}
		before, err := file.Stat()
		if err != nil {
			file.Close()
			return FileSnapshot{}, err
		}
		cursor, leaf, complete, err := lastAppendCursor(file, before.Size())
		after, afterErr := file.Stat()
		closeErr := file.Close()
		current, currentErr := os.Stat(path)
		if err != nil {
			return FileSnapshot{}, err
		}
		if afterErr != nil {
			return FileSnapshot{}, afterErr
		}
		if closeErr != nil {
			return FileSnapshot{}, closeErr
		}
		if currentErr != nil {
			return FileSnapshot{}, currentErr
		}
		if sameFileStat(before, after) && sameFileStat(after, current) {
			device, inode := fileIdentity(after)
			return FileSnapshot{Device: device, Inode: inode, Size: after.Size(), MTimeNS: after.ModTime().UnixNano(), AppendCursor: cursor, PersistedLeafID: leaf, Complete: complete}, nil
		}
	}
	return FileSnapshot{}, errors.New("session file kept changing while it was read")
}

func lastAppendCursor(file *os.File, size int64) (string, string, bool, error) {
	complete := true
	var fragments [][]byte
	var fragmentBytes int64
	position := size
	for position > 0 {
		length := int64(8 << 10)
		if position < length {
			length = position
		}
		position -= length
		chunk := make([]byte, length)
		if _, err := file.ReadAt(chunk, position); err != nil && !errors.Is(err, io.EOF) {
			return "", "", false, err
		}
		lineEnd := len(chunk)
		for lineEnd > 0 {
			newline := bytes.LastIndexByte(chunk[:lineEnd], '\n')
			if newline < 0 {
				break
			}
			parts := make([][]byte, 0, len(fragments)+1)
			parts = append(parts, chunk[newline+1:lineEnd])
			for index := len(fragments) - 1; index >= 0; index-- {
				parts = append(parts, fragments[index])
			}
			total := int64(lineEnd-newline-1) + fragmentBytes
			if total > MaxRenderedEntryBytes {
				return "", "", false, errEntryOverCap
			}
			if cursor, leaf, found, valid := appendCursorFromParts(parts, total); found {
				return cursor, leaf, complete && valid, nil
			} else if !valid {
				complete = false
			}
			fragments, fragmentBytes = nil, 0
			lineEnd = newline
		}
		if lineEnd > 0 {
			fragment := append([]byte(nil), chunk[:lineEnd]...)
			fragments = append(fragments, fragment)
			fragmentBytes += int64(len(fragment))
			if fragmentBytes > MaxRenderedEntryBytes {
				return "", "", false, errEntryOverCap
			}
		}
	}
	parts := make([][]byte, 0, len(fragments))
	for index := len(fragments) - 1; index >= 0; index-- {
		parts = append(parts, fragments[index])
	}
	if cursor, leaf, found, valid := appendCursorFromParts(parts, fragmentBytes); found {
		return cursor, leaf, complete && valid, nil
	} else if !valid {
		complete = false
	}
	return "", "", complete, nil
}

func appendCursorFromParts(parts [][]byte, total int64) (cursor, leaf string, found, valid bool) {
	if total <= MaxIndexedEntryBytes {
		line := make([]byte, 0, total)
		for _, part := range parts {
			line = append(line, part...)
		}
		return appendCursorFromLine(line)
	}
	scanner := newIndexJSONScanner()
	for _, part := range parts {
		scanner.feed(part)
	}
	entry, ok := scanner.finish()
	if !ok {
		line := make([]byte, 0, total)
		for _, part := range parts {
			line = append(line, part...)
		}
		return appendCursorFromLine(line)
	}
	if entry.ID == "" || entry.Type == "session" {
		return "", "", false, true
	}
	leaf = entry.ID
	if entry.Type == "leaf" {
		leaf = entry.TargetID
	}
	return entry.ID, leaf, true, true
}

func appendCursorFromLine(line []byte) (cursor, leaf string, found, valid bool) {
	if len(bytes.TrimSpace(line)) == 0 {
		return "", "", false, true
	}
	var entry map[string]any
	if json.Unmarshal(line, &entry) != nil {
		return "", "", false, false
	}
	id, _ := entry["id"].(string)
	if id == "" || entry["type"] == "session" {
		return "", "", false, true
	}
	leaf = id
	if entry["type"] == "leaf" {
		leaf, _ = entry["targetId"].(string)
	}
	return id, leaf, true, true
}

func (store Store) AppendedEntryIDs(path string, previous, current FileSnapshot) ([]string, error) {
	length := current.Size - previous.Size
	if length <= 0 {
		return nil, nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return nil, err
	}
	device, inode := fileIdentity(stat)
	if device != current.Device || inode != current.Inode || stat.Size() < current.Size {
		return nil, errors.New("session file changed while appended entries were read")
	}
	if _, err := file.Seek(previous.Size, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(io.LimitReader(file, length))
	result := []string{}
	offset := previous.Size
	for {
		line, lineLength, largeEntry, readErr := readIndexedLine(reader, file, offset)
		offset += lineLength
		if lineLength == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		id := ""
		if largeEntry != nil {
			id = largeEntry.ID
		} else if len(bytes.TrimSpace(line)) > 0 {
			var entry map[string]any
			if err := json.Unmarshal(line, &entry); err != nil {
				return nil, err
			}
			id, _ = entry["id"].(string)
		} else {
			if errors.Is(readErr, io.EOF) {
				break
			}
			continue
		}
		if id == "" {
			return nil, errors.New("appended session entry is missing a string id")
		}
		result = append(result, id)
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return result, nil
}

func sameFileStat(left, right os.FileInfo) bool {
	if left == nil || right == nil {
		return false
	}
	leftDevice, leftInode := fileIdentity(left)
	rightDevice, rightInode := fileIdentity(right)
	return leftDevice == rightDevice && leftInode == rightInode && left.Size() == right.Size() && left.ModTime().UnixNano() == right.ModTime().UnixNano()
}
