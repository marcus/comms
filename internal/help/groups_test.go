package help

import "testing"

func TestGroupOpenAPIDocumentsRecipientAlternatives(t *testing.T) {
	paths := OpenAPIDocument()["paths"].(map[string]any)
	operation := paths["/v1/direct-messages"].(map[string]any)["post"].(map[string]any)
	body := operation["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
	if len(body["oneOf"].([]any)) != 2 {
		t.Fatal(body)
	}
	props := body["properties"].(map[string]any)
	array := props["agents"].(map[string]any)
	if array["type"] != "array" || array["minItems"] != 1 || array["maxItems"] != 31 || array["items"].(map[string]any)["type"] != "string" {
		t.Fatal(array)
	}
	for _, name := range body["required"].([]string) {
		if name == "agent" || name == "agents" {
			t.Fatal("alternatives must not both be required", body)
		}
	}
}
