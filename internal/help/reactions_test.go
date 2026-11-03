package help

import (
	"strings"
	"testing"
)

func TestReactionContractsAcrossGeneratedSurfaces(t *testing.T) {
	doc := OpenAPIDocument()
	paths := doc["paths"].(map[string]any)
	operation := paths["/v1/messages/{message}/reactions"].(map[string]any)["post"].(map[string]any)
	body := operation["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	props := body["properties"].(map[string]any)
	for _, field := range []string{"reaction", "remove", "client_id", "request_id"} {
		if props[field] == nil {
			t.Fatalf("react omits %s", field)
		}
	}
	data := operation["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)["data"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"message_id", "reaction", "removed", "changed"} {
		if data[field] == nil {
			t.Fatalf("react response omits %s", field)
		}
	}
	for _, path := range []string{"/v1/inbox", "/v1/wait", "/v1/observe"} {
		op := paths[path].(map[string]any)["get"].(map[string]any)
		names := map[string]bool{}
		for _, p := range op["parameters"].([]any) {
			name, _ := p.(map[string]any)["name"].(string)
			names[name] = true
		}
		if !names["include_reactions"] || !names["reactions_after"] {
			t.Fatalf("%s parameters=%v", path, names)
		}
		response := op["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)["data"].(map[string]any)["properties"].(map[string]any)
		if response["reaction_events"] == nil || response["reactions_after"] == nil {
			t.Fatalf("%s response=%v", path, response)
		}
	}
	for _, command := range []string{"react", "inbox", "wait", "observe"} {
		text, err := CommandHelp("comms", command)
		if err != nil {
			t.Fatal(err)
		}
		if command == "react" {
			if !strings.Contains(text, "--remove") {
				t.Fatal(text)
			}
		} else if !strings.Contains(text, "--include-reactions") || !strings.Contains(text, "--reactions-after") {
			t.Fatal(text)
		}
	}
}
