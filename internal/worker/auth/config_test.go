package auth

import (
	"encoding/json"
	"reflect"
	"slices"
	"testing"
)

func TestPiCommandLeavesOutTheOptions(t *testing.T) {
	for _, test := range []struct{ command, want []string }{
		{[]string{"pi"}, []string{"pi"}},
		{[]string{"pi", "--approve"}, []string{"pi"}},
		{[]string{"/usr/bin/node", "/lib/pi/dist/cli.js", "--approve", "--mode", "rpc"}, []string{"/usr/bin/node", "/lib/pi/dist/cli.js"}},
	} {
		if got := PiCommand(test.command); !slices.Equal(got, test.want) {
			t.Errorf("PiCommand(%q) = %q, want %q", test.command, got, test.want)
		}
	}
}

func TestPlainJSONReadsWhatPiAllowsInItsConfiguration(t *testing.T) {
	data := "\ufeff" + `{
  // A comment, unlike the // in the URL.
  "providers": {"proxy": {"baseUrl": "http://localhost:8317/v1", "models": [{"id": "a,]"},],},},
}`
	var parsed any
	if err := json.Unmarshal(PlainJSON([]byte(data)), &parsed); err != nil {
		t.Fatalf("%s: %v", PlainJSON([]byte(data)), err)
	}
	want := map[string]any{"providers": map[string]any{"proxy": map[string]any{"baseUrl": "http://localhost:8317/v1", "models": []any{map[string]any{"id": "a,]"}}}}}
	if !reflect.DeepEqual(parsed, want) {
		t.Fatalf("parsed %#v, want %#v", parsed, want)
	}
}
