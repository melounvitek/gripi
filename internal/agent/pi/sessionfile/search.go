package sessionfile

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/melounvitek/gripi/internal/agent"
)

// searchSnippetBytes is how much text a match shows on each side of the query.
const searchSnippetBytes = 80

// Search reads the whole file, with the branches that /tree left behind: what the user sent, Pi's replies, its tool
// calls with their arguments and their results, the output of shell commands and the messages of extensions. Thinking
// and images are left out. Matches have the roles of gripi show: user, assistant, tool and custom.
func (store Store) Search(key, query string, roles []string, limit int) (agent.SearchResult, error) {
	path, ok := store.canonicalSessionPath(key)
	if !ok {
		return agent.SearchResult{}, os.ErrNotExist
	}
	file, err := os.Open(path)
	if err != nil {
		return agent.SearchResult{}, err
	}
	defer file.Close()
	query = strings.ToLower(query)
	scanner := lineScanner{reader: bufio.NewReaderSize(file, 64<<10), needle: searchNeedle(query)}
	var result agent.SearchResult
	for offset := int64(0); ; {
		found, length, err := scanner.next()
		// Only a line that holds the query as JSON writes it is parsed. A larger one than the gateway renders is
		// mostly images, and would cost as much memory.
		if found && length <= MaxRenderedEntryBytes {
			line := make([]byte, length)
			if _, readErr := file.ReadAt(line, offset); readErr != nil && !errors.Is(readErr, io.EOF) {
				return agent.SearchResult{}, readErr
			}
			for _, match := range searchEntry(line, query, roles) {
				result.Count++
				result.Matches = append(result.Matches, match)
				if len(result.Matches) > limit {
					result.Matches = result.Matches[1:]
				}
			}
		}
		offset += length
		if errors.Is(err, io.EOF) {
			return result, nil
		}
		if err != nil {
			return agent.SearchResult{}, err
		}
	}
}

// searchNeedle is the longest part of the query that a JSON line holds as it is, so that most lines are left without
// being parsed. JSON escapes quotes, backslashes and control characters, and the scanner ignores the case of ASCII
// letters only, so the needle has none of these characters and no other non-ASCII ones.
func searchNeedle(query string) []byte {
	longestStart, longestEnd, start := 0, 0, 0
	for index := 0; index <= len(query); index++ {
		if index < len(query) && query[index] >= ' ' && query[index] <= '~' && query[index] != '"' && query[index] != '\\' {
			continue
		}
		if index-start > longestEnd-longestStart {
			longestStart, longestEnd = start, index
		}
		start = index + 1
	}
	return []byte(query[longestStart:longestEnd])
}

// lineScanner reads a file line by line, however long the lines are, and tells which lines hold the needle, ignoring
// the case of ASCII letters.
type lineScanner struct {
	reader *bufio.Reader
	needle []byte
	// window is the lowered end of the line read so far, where a match may begin.
	window []byte
}

// next reads a line, with its newline, and reports whether it holds the needle and how long it is.
func (scanner *lineScanner) next() (found bool, length int64, err error) {
	scanner.window = scanner.window[:0]
	found = len(scanner.needle) == 0
	for {
		part, err := scanner.reader.ReadSlice('\n')
		length += int64(len(part))
		if !found {
			for _, character := range part {
				if 'A' <= character && character <= 'Z' {
					character += 'a' - 'A'
				}
				scanner.window = append(scanner.window, character)
			}
			found = bytes.Contains(scanner.window, scanner.needle)
			// Only the part of the line that could begin a match that the next part ends stays.
			keep := len(scanner.needle) - 1
			scanner.window = append(scanner.window[:0], scanner.window[max(0, len(scanner.window)-keep):]...)
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return found, length, err
		}
	}
}

// searchEntry returns the matches of a lowered query in one line of a session file.
func searchEntry(line []byte, query string, roles []string) []agent.SearchMatch {
	var raw map[string]any
	if json.Unmarshal(line, &raw) != nil {
		return nil
	}
	type text struct{ role, value string }
	var texts []text
	message := asMap(raw["message"])
	switch {
	case stringValue(raw["type"]) == "custom_message":
		texts = append(texts, text{"custom", searchContentText(raw["content"])})
	case stringValue(raw["type"]) != "message":
	case stringValue(message["role"]) == "user":
		texts = append(texts, text{"user", searchContentText(message["content"])})
	case stringValue(message["role"]) == "assistant":
		texts = append(texts, text{"assistant", searchContentText(message["content"])})
		for _, part := range arrayValue(message["content"]) {
			call := asMap(part)
			if stringValue(call["type"]) == "toolCall" {
				// As Pi writes them, so that a URL keeps its & as it was.
				var arguments strings.Builder
				encoder := json.NewEncoder(&arguments)
				encoder.SetEscapeHTML(false)
				encoder.Encode(call["arguments"])
				texts = append(texts, text{"tool", stringValue(call["name"]) + " " + strings.TrimSpace(arguments.String())})
			}
		}
	case stringValue(message["role"]) == "toolResult":
		texts = append(texts, text{"tool", searchContentText(message["content"])})
	case stringValue(message["role"]) == "bashExecution":
		texts = append(texts, text{"tool", "$ " + stringValue(message["command"]) + "\n" + stringValue(message["output"])})
	}
	var matches []agent.SearchMatch
	for _, candidate := range texts {
		if len(roles) > 0 && !slices.Contains(roles, candidate.role) {
			continue
		}
		if snippet, found := searchSnippet(candidate.value, query); found {
			matches = append(matches, agent.SearchMatch{Role: candidate.role, Snippet: snippet, Timestamp: parseTime(stringValue(raw["timestamp"]))})
		}
	}
	return matches
}

// searchContentText joins the text of a message, without its thinking, tool calls and images.
func searchContentText(content any) string {
	var values []string
	for _, part := range arrayValue(content) {
		switch value := part.(type) {
		case string:
			values = append(values, value)
		case map[string]any:
			if stringValue(value["type"]) == "text" {
				values = append(values, stringValue(value["text"]))
			}
		}
	}
	return strings.Join(values, "\n")
}

// searchSnippet finds a lowered query in text and returns the text around it on one line.
func searchSnippet(text, query string) (string, bool) {
	lowered := strings.ToLower(text)
	index := strings.Index(lowered, query)
	if index < 0 {
		return "", false
	}
	// Lowering changes the length of a few characters; the snippet then comes from the lowered text.
	if len(lowered) != len(text) {
		text = lowered
	}
	start, end := max(0, index-searchSnippetBytes), min(len(text), index+len(query)+searchSnippetBytes)
	for start > 0 && !utf8.RuneStart(text[start]) {
		start--
	}
	for end < len(text) && !utf8.RuneStart(text[end]) {
		end++
	}
	snippet := strings.Join(strings.Fields(text[start:end]), " ")
	if start > 0 {
		snippet = "…" + snippet
	}
	if end < len(text) {
		snippet += "…"
	}
	return snippet, true
}
