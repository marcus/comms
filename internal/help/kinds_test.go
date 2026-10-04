package help

import (
	"reflect"
	"testing"
)

func TestMessageKindAndNullableReplyTitleContract(t *testing.T) {
	paths := OpenAPIDocument()["paths"].(map[string]any)
	for _, path := range []string{"/v1/messages", "/v1/direct-messages", "/v1/messages/{message}/replies"} {
		op := paths[path].(map[string]any)["post"].(map[string]any)
		schema := op["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		properties := schema["properties"].(map[string]any)
		kind := properties["kind"].(map[string]any)
		if kind["maxLength"] != 64 || kind["pattern"] != "^([a-z0-9][a-z0-9_.-]{0,63})?$" {
			t.Fatalf("kind contract=%#v", kind)
		}
		title := properties["title"].(map[string]any)
		if path == "/v1/messages/{message}/replies" && !reflect.DeepEqual(title["type"], []string{"string", "null"}) {
			t.Fatalf("reply title=%#v", title)
		}
	}
	for _, path := range []string{"/v1/inbox", "/v1/wait"} {
		op := paths[path].(map[string]any)["get"].(map[string]any)
		found := false
		for _, raw := range op["parameters"].([]any) {
			if raw.(map[string]any)["name"] == "kind" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s missing kind", path)
		}
	}
}
