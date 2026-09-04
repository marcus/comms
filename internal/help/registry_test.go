package help

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRegistryIsValidAndComplete(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}

	wantIDs := []string{
		"service.serve", "store.handshake", "capability.describe", "instructions.generate", "health.get", "doctor.run", "version.get",
		"agent.join", "agent.whoami", "agent.get", "agent.update", "agent.retire", "agent.list",
		"topic.create", "topic.ensure", "topic.update", "topic.archive", "topic.list", "subscription.follow", "subscription.unfollow", "subscription.list",
		"message.publish", "message.direct_send", "message.reply", "message.inbox", "message.topic", "message.thread", "message.peek", "message.read_through", "message.receipts", "message.search", "message.observe",
		"retention.status", "retention.purge", "diagnostic.export",
	}
	gotIDs := make([]string, 0, len(registry))
	for _, operation := range Operations() {
		gotIDs = append(gotIDs, operation.ID)
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("operation IDs = %#v, want %#v", gotIDs, wantIDs)
	}
}

func TestGeneratedSurfacesHaveRegistryParity(t *testing.T) {
	usage := CLIUsage("comms")
	instructions := AgentInstructions()
	instructionCommands := make(map[string]struct{}, len(instructions.Commands))
	for _, command := range instructions.Commands {
		instructionCommands[command] = struct{}{}
	}

	openAPI := OpenAPIDocument()
	paths := openAPI["paths"].(map[string]any)
	tools := make(map[string]MCPToolDescription)
	for _, tool := range MCPTools() {
		tools[tool.Name] = tool
	}

	for _, operation := range Operations() {
		if operation.CLI != "" {
			command := "comms " + operation.CLI
			if !strings.Contains(usage, command) {
				t.Errorf("CLI usage omits %q", command)
			}
			if _, ok := instructionCommands[command]; !ok {
				t.Errorf("instructions omit registry command %q", command)
			}
		}
		if operation.HTTP != nil {
			pathItem, ok := paths[operation.HTTP.Path].(map[string]any)
			if !ok {
				t.Errorf("OpenAPI omits path %q", operation.HTTP.Path)
				continue
			}
			generated, ok := pathItem[strings.ToLower(operation.HTTP.Method)].(map[string]any)
			if !ok {
				t.Errorf("OpenAPI omits %s", HTTPRouteKey(operation))
				continue
			}
			if got := generated["operationId"]; got != operation.ID {
				t.Errorf("OpenAPI operation ID for %s = %v, want %q", HTTPRouteKey(operation), got, operation.ID)
			}
			if operation.Mutating {
				assertOpenAPIIdempotencyInputs(t, operation, generated)
			}
			if operation.RequiresIdentity {
				assertOpenAPIIdentityHeader(t, operation, generated)
			}
		}
		if operation.MCPName != "" {
			tool, ok := tools[operation.MCPName]
			if !ok {
				t.Errorf("MCP tools omit %q", operation.MCPName)
				continue
			}
			if got, want := tool.Annotations.ReadOnlyHint, !operation.Mutating; got != want {
				t.Errorf("MCP readOnlyHint for %q = %v, want %v", operation.MCPName, got, want)
			}
			if operation.Mutating {
				for _, name := range []string{"client_id", "request_id"} {
					if _, ok := tool.InputSchema.Properties[name]; !ok {
						t.Errorf("MCP tool %q omits idempotency input %q", operation.MCPName, name)
					}
				}
			}
		}
	}
}

func assertOpenAPIIdentityHeader(t *testing.T, operation Operation, generated map[string]any) {
	t.Helper()
	parameters, ok := generated["parameters"].([]any)
	if !ok {
		t.Errorf("OpenAPI identity-bearing operation %q has no parameters", operation.ID)
		return
	}
	for _, raw := range parameters {
		parameter, ok := raw.(map[string]any)
		if ok && parameter["$ref"] == "#/components/parameters/AgentIdentity" {
			return
		}
	}
	t.Errorf("OpenAPI identity-bearing operation %q omits agent identity header", operation.ID)
}

func assertOpenAPIIdempotencyInputs(t *testing.T, operation Operation, generated map[string]any) {
	t.Helper()
	requestBody, ok := generated["requestBody"].(map[string]any)
	if !ok {
		t.Errorf("OpenAPI mutation %q has no request body for idempotency inputs", operation.ID)
		return
	}
	content := requestBody["content"].(map[string]any)
	media := content["application/json"].(map[string]any)
	schema := media["schema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	for _, name := range []string{"client_id", "request_id"} {
		if _, ok := properties[name]; !ok {
			t.Errorf("OpenAPI mutation %q omits idempotency input %q", operation.ID, name)
		}
	}
}

func TestGeneratedDocumentsAreVersionedValidJSON(t *testing.T) {
	tests := []struct {
		name string
		make func() ([]byte, error)
		want string
	}{
		{name: "capabilities", make: CapabilitiesJSON, want: `"schema": "` + CapabilitySchema + `"`},
		{name: "instructions", make: InstructionsJSON, want: `"schema": "` + InstructionsSchema + `"`},
		{name: "openapi", make: OpenAPIJSON, want: `"openapi": "3.1.0"`},
		{name: "mcp tools", make: MCPToolsJSON, want: `"name": "comms_handshake"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			first, err := test.make()
			if err != nil {
				t.Fatal(err)
			}
			second, err := test.make()
			if err != nil {
				t.Fatal(err)
			}
			if string(first) != string(second) {
				t.Fatal("generated document is not deterministic")
			}
			if !json.Valid(first) {
				t.Fatal("generated document is not valid JSON")
			}
			if !strings.Contains(string(first), test.want) {
				t.Errorf("generated document omits %q", test.want)
			}
		})
	}
}

func TestInstructionsStateCursorAndTrustBoundaries(t *testing.T) {
	text := InstructionsText()
	for _, want := range []string{
		"do not advance read cursors",
		"acknowledges all earlier visible sequences",
		"not trusted-local read access",
		"does not spawn agents",
		"durable tasks, decisions, artifacts, and secrets",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("instructions omit %q", want)
		}
	}
	for _, unwanted := range []string{"poll every", "on startup", "before handoff"} {
		if strings.Contains(strings.ToLower(text), unwanted) {
			t.Errorf("instructions prescribe lifecycle behavior %q", unwanted)
		}
	}
}

func TestOperationsReturnsDefensiveCopy(t *testing.T) {
	operations := Operations()
	operations[0].ID = "changed"
	operations[7].Parameters[0].Name = "changed"
	operations[7].Parameters[0].Enum = []string{"changed"}
	operations[1].HTTP.Path = "/changed"

	fresh := Operations()
	if fresh[0].ID == "changed" || fresh[7].Parameters[0].Name == "changed" || fresh[1].HTTP.Path == "/changed" {
		t.Fatal("Operations exposed mutable registry state")
	}
}
