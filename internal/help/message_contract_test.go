package help

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBridgeAndMessageContractsAreDiscoverable(t *testing.T) {
	data, err := OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	paths := doc["paths"].(map[string]any)
	if _, ok := paths["/v1/topics/{topic}"].(map[string]any)["get"]; !ok {
		t.Fatal("missing topic lookup")
	}
	for _, token := range []string{`"author_handle"`, `"author_display_name"`, `"recipient_ids"`, `"members"`, `"after"`, `"wait"`, `"topic"`} {
		if !strings.Contains(string(data), token) {
			t.Fatalf("missing schema field %s", token)
		}
	}
	helpText, err := CommandHelp("comms", "observe")
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"--wait", "--after", "--topic", "--timeout", "recipient_ids"} {
		if !strings.Contains(helpText, token) {
			t.Fatalf("missing help %s", token)
		}
	}
}
