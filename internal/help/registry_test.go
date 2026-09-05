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
		"service.serve", "service.status", "service.stop", "service.restart", "service.shutdown", "store.handshake", "capability.describe", "api.openapi", "instructions.generate", "health.get", "doctor.run", "version.get",
		"agent.join", "agent.whoami", "agent.get", "agent.update", "agent.retire", "agent.list", "agent.wait",
		"topic.create", "topic.ensure", "topic.update", "topic.archive", "topic.list", "subscription.follow", "subscription.unfollow", "subscription.list",
		"message.publish", "message.direct_send", "message.reply", "message.inbox", "message.wait", "message.topic", "message.thread", "message.peek", "message.read_through", "message.receipts", "message.search", "message.observe",
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

func TestOpenAPIModelsActualSuccessContracts(t *testing.T) {
	document := OpenAPIDocument()
	paths := document["paths"].(map[string]any)
	agentGet := paths["/v1/agents/{agent}"].(map[string]any)["get"].(map[string]any)
	success := agentGet["responses"].(map[string]any)["200"].(map[string]any)
	content := success["content"].(map[string]any)
	schema := content["application/json"].(map[string]any)["schema"].(map[string]any)
	properties := schema["properties"].(map[string]any)
	if properties["schema"].(map[string]any)["const"] != ResponseSchema {
		t.Fatalf("response schema=%#v", properties["schema"])
	}
	data := properties["data"].(map[string]any)
	agentProperties := data["properties"].(map[string]any)
	for _, field := range []string{"id", "handle", "created_at", "retired_at"} {
		if _, ok := agentProperties[field]; !ok {
			t.Errorf("agent response omits %s", field)
		}
	}

	export := paths["/v1/export"].(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)
	if _, ok := export["application/x-ndjson"]; !ok {
		t.Fatalf("export content=%#v", export)
	}
	if _, ok := export["application/json"]; ok {
		t.Fatalf("export incorrectly claims JSON response")
	}

	hello := paths["/v1/hello"].(map[string]any)["get"].(map[string]any)["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)["data"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"store_id", "protocol_version", "schema_version", "server_instance_id", "pid", "started_at", "launch_mode", "commit", "socket_path", "database_path", "capabilities"} {
		if _, ok := hello[field]; !ok {
			t.Errorf("handshake response omits %s", field)
		}
	}

	shutdown := paths["/v1/admin/shutdown"].(map[string]any)["post"].(map[string]any)
	if _, ok := shutdown["responses"].(map[string]any)["202"]; !ok {
		t.Fatalf("shutdown responses=%#v", shutdown["responses"])
	}
	if _, ok := shutdown["responses"].(map[string]any)["200"]; ok {
		t.Fatal("shutdown should not claim a 200 response")
	}

	for _, operation := range Operations() {
		if operation.HTTP == nil || operation.ID == "diagnostic.export" {
			continue
		}
		if responseType(operation.ID) == nil {
			t.Errorf("operation %s has no success response type", operation.ID)
		}
	}
}

func TestCLIUsageMentionsAutoStart(t *testing.T) {
	usage := CLIUsage("comms")
	if strings.Contains(usage, "Stateful commands require") {
		t.Fatal("usage still requires an explicit comms serve")
	}
	for _, want := range []string{
		"Ordinary commands start the local service if needed",
		"status, health, hello, doctor, and stop do not",
		"comms serve",
		"brew services start comms",
		"comms status",
		"comms stop",
		"comms restart",
	} {
		if !strings.Contains(usage, want) {
			t.Errorf("CLI usage omits %q", want)
		}
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
	var parameterized *Operation
	for i := range operations {
		if len(operations[i].Parameters) > 0 {
			parameterized = &operations[i]
			break
		}
	}
	if parameterized == nil {
		t.Fatal("no parameterized operation")
	}
	parameterized.Parameters[0].Name = "changed"
	parameterized.Parameters[0].Enum = []string{"changed"}
	httpIndex := -1
	for i := range operations {
		if operations[i].HTTP != nil {
			httpIndex = i
			break
		}
	}
	if httpIndex < 0 {
		t.Fatal("expected an HTTP operation")
	}
	operations[httpIndex].HTTP.Path = "/changed"

	fresh := Operations()
	if fresh[0].ID == "changed" || fresh[httpIndex].HTTP.Path == "/changed" {
		t.Fatal("Operations exposed mutable registry state")
	}
	for _, operation := range fresh {
		if len(operation.Parameters) > 0 && operation.Parameters[0].Name == "changed" {
			t.Fatal("Operations exposed mutable registry state")
		}
	}
}
