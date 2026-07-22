package sessions

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
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

type AttachmentStore struct{ Root string }

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
	file, err := os.Open(filepath.Join(store.Root, SessionHash(sessionPath)+".jsonl"))
	if err != nil {
		return nil
	}
	defer file.Close()
	var result []Attachment
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		var attachment Attachment
		if json.Unmarshal(scanner.Bytes(), &attachment) == nil {
			result = append(result, attachment)
		}
	}
	return result
}
