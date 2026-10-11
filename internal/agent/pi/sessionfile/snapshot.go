package sessionfile

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

func readFileSnapshot(path string) (FileSnapshot, error) {
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

// lastAppendCursor finds the lines from the end backward and reads each like
// the index does, so a line of any size is read in bounded memory.
func lastAppendCursor(file *os.File, size int64) (string, string, bool, error) {
	complete := true
	for end := size; end > 0; {
		start, err := lineStart(file, end)
		if err != nil {
			return "", "", false, err
		}
		cursor, leaf, found, valid, err := appendCursorAt(file, start, end)
		if err != nil {
			return "", "", false, err
		}
		complete = complete && valid
		if found {
			return cursor, leaf, complete, nil
		}
		end = start - 1
	}
	return "", "", complete, nil
}

// lineStart returns where the line that ends at end begins.
func lineStart(file *os.File, end int64) (int64, error) {
	chunk := make([]byte, 8<<10)
	for position := end; position > 0; {
		length := min(int64(len(chunk)), position)
		position -= length
		if _, err := file.ReadAt(chunk[:length], position); err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
		if newline := bytes.LastIndexByte(chunk[:length], '\n'); newline >= 0 {
			return position + int64(newline) + 1, nil
		}
	}
	return 0, nil
}

func appendCursorAt(file *os.File, start, end int64) (cursor, leaf string, found, valid bool, err error) {
	section := io.NewSectionReader(file, start, end-start)
	var line []byte
	var large *entry
	if end-start > MaxRenderedEntryBytes {
		// Unlike the index, this knows the length, so the stub is read at once.
		stub, ok := scanOversizedEntry(section)
		if !ok {
			return "", "", false, false, nil
		}
		large = &stub
	} else {
		line, _, large, err = readIndexedLine(bufio.NewReader(section), file, start)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", "", false, false, err
		}
	}
	if large == nil {
		cursor, leaf, found, valid = appendCursorFromLine(line)
		return cursor, leaf, found, valid, nil
	}
	if large.ID == "" || large.Type == "session" {
		return "", "", false, true, nil
	}
	leaf = large.ID
	if large.Type == "leaf" {
		leaf = large.TargetID
	}
	return large.ID, leaf, true, true, nil
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

func appendedEntryIDs(path string, previous, current FileSnapshot) ([]string, error) {
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
