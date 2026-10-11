package sessionfile

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOversizedEntryScannerAcceptsOnlyWellFormedObjects(t *testing.T) {
	escaped := `{"type":"message","id":"a\"b\u00e9","parentId":null,"message":{"role":"toolResult","toolCallId":"call","content":[{"n":-1.5e3,"t":true,"f":false,"z":null,"s":"\\\/\b\f\n\r\t"}]}}`
	for _, line := range []string{
		escaped, `{}`, `{"id":"x",}`, `{"id":"x"`, `{"id":"x"}}`, `{"id":"x"} x`, `["id"]`, `"id"`, `{"id":"\x"}`, `{"id":"\u12g4"}`,
		"{\"id\":\"a\tb\"}", `{"n":01}`, `{"t":tru}`, `{"a":[1,]}`, `{"a" 1}`, `{,}`, strings.Repeat("[", 50) + strings.Repeat("]", 50),
		`{"a":` + strings.Repeat("[", 99) + strings.Repeat("]", 99) + `}`, `{"a":` + strings.Repeat("[", 100) + strings.Repeat("]", 100) + `}`,
	} {
		_, ok := scanOversizedEntry(strings.NewReader(line + "\n"))
		// Like the index scanner, it gives up past 100 levels.
		want := json.Valid([]byte(line)) && strings.HasPrefix(line, "{") && strings.Count(line, "[") < 100
		if ok != want {
			t.Errorf("scanOversizedEntry(%.80s) = %v, want %v", line, ok, want)
		}
	}

	stub, _ := scanOversizedEntry(strings.NewReader(escaped))
	if stub.Type != "message" || stub.ID != "a\"bé" || stub.ParentID != "" || stub.Role != "toolResult" || stub.ResultToolCallID != "call" || len(stub.Segments) != 1 {
		t.Fatalf("stub = %#v", stub)
	}
}
