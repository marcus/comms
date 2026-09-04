package help

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// CapabilityOperation is the stable, versioned discovery shape exposed to
// clients. Parameters remain transport-neutral; bindings describe available
// projections without making a transport authoritative.
type CapabilityOperation struct {
	ID               string       `json:"id"`
	Category         string       `json:"category"`
	Summary          string       `json:"summary"`
	Description      string       `json:"description"`
	Mutating         bool         `json:"mutating"`
	RequiresIdentity bool         `json:"requires_identity"`
	UnixOnly         bool         `json:"unix_only,omitempty"`
	CLI              string       `json:"cli,omitempty"`
	HTTP             *HTTPBinding `json:"http,omitempty"`
	MCPTool          string       `json:"mcp_tool,omitempty"`
	Parameters       []Parameter  `json:"parameters,omitempty"`
}

// CapabilityDescription is the stable discovery response generated from the
// authored registry.
type CapabilityDescription struct {
	Schema          string                `json:"schema"`
	ProtocolVersion string                `json:"protocol_version"`
	Operations      []CapabilityOperation `json:"operations"`
}

// Capabilities builds the versioned capability description.
func Capabilities() CapabilityDescription {
	operations := Operations()
	description := CapabilityDescription{
		Schema:          CapabilitySchema,
		ProtocolVersion: ProtocolVersion,
		Operations:      make([]CapabilityOperation, 0, len(operations)),
	}
	for _, operation := range operations {
		description.Operations = append(description.Operations, CapabilityOperation{
			ID:               operation.ID,
			Category:         operation.Category,
			Summary:          operation.Summary,
			Description:      operation.Description,
			Mutating:         operation.Mutating,
			RequiresIdentity: operation.RequiresIdentity,
			UnixOnly:         operation.UnixOnly,
			CLI:              operation.CLI,
			HTTP:             operation.HTTP,
			MCPTool:          operation.MCPName,
			Parameters:       operation.Parameters,
		})
	}
	return description
}

// CapabilitiesJSON renders the versioned capability description as
// deterministic, indented JSON.
func CapabilitiesJSON() ([]byte, error) {
	return marshalDocument(Capabilities())
}

// CLIUsage generates human command help from the registry.
func CLIUsage(program string) string {
	if program == "" {
		program = "comms"
	}
	var output strings.Builder
	fmt.Fprintln(&output, "Comms connects independent agent sessions through short-lived local topics.")
	fmt.Fprintln(&output)
	fmt.Fprintf(&output, "Usage:\n  %s [--json] [--as AGENT] COMMAND\n", program)

	category := ""
	for _, operation := range Operations() {
		if operation.CLI == "" {
			continue
		}
		if operation.Category != category {
			category = operation.Category
			fmt.Fprintf(&output, "\n%s:\n", category)
		}
		fmt.Fprintf(&output, "  %-92s %s\n", program+" "+operation.CLI, operation.Summary)
	}
	fmt.Fprintln(&output, "\nEvery response supports --json. Stateful commands require `comms serve`.")
	return output.String()
}

// Instructions describes how agents can use Comms without prescribing a
// harness lifecycle, polling cadence, or handoff workflow.
type Instructions struct {
	Schema     string   `json:"schema"`
	Purpose    string   `json:"purpose"`
	Guarantees []string `json:"guarantees"`
	Boundaries []string `json:"boundaries"`
	Examples   []string `json:"examples"`
	Commands   []string `json:"commands"`
}

// AgentInstructions builds versioned structured agent guidance.
func AgentInstructions() Instructions {
	commands := make([]string, 0, len(registry))
	for _, operation := range Operations() {
		if operation.CLI != "" {
			commands = append(commands, "comms "+operation.CLI)
		}
	}
	return Instructions{
		Schema:  InstructionsSchema,
		Purpose: "Exchange short-lived messages and pointers among independent local agent sessions.",
		Guarantees: []string{
			"A successful publish has been accepted into the authoritative store.",
			"Inbox, peek, thread, search, receipts, and observe do not advance read cursors.",
			"Read-through advances one topic cursor through the named message and acknowledges all earlier visible sequences.",
			"Stable record IDs do not change when friendly handles or topic names change.",
			"Structured responses, error codes, and cursor meanings follow versioned compatibility contracts.",
		},
		Boundaries: []string{
			"Comms carries transient coordination; durable tasks, decisions, artifacts, and secrets belong in their owning systems.",
			"Delivery does not prove that a session is alive, noticed a message, understood it, or acted on it.",
			"Direct topics control routing and ordinary discovery, not trusted-local read access.",
			"Comms does not spawn agents, assign work, execute messages, or replace project documentation and task trackers.",
		},
		Examples: []string{
			"comms join build-agent --harness codex --context ./comms-context.json",
			"printf '%s\\n' 'Review notes are in td-123abc.' | comms publish project-alpha --title 'Review ready' -",
			"comms inbox --unread --json",
			"comms read-through msg_example --json",
		},
		Commands: commands,
	}
}

// InstructionsJSON renders the versioned structured agent instructions.
func InstructionsJSON() ([]byte, error) {
	return marshalDocument(AgentInstructions())
}

// InstructionsText generates concise human-readable agent guidance.
func InstructionsText() string {
	instructions := AgentInstructions()
	var output strings.Builder
	fmt.Fprintln(&output, "Comms agent instructions")
	fmt.Fprintln(&output)
	fmt.Fprintln(&output, instructions.Purpose)
	fmt.Fprintln(&output, "\nGuarantees:")
	for _, guarantee := range instructions.Guarantees {
		fmt.Fprintf(&output, "- %s\n", guarantee)
	}
	fmt.Fprintln(&output, "\nBoundaries:")
	for _, boundary := range instructions.Boundaries {
		fmt.Fprintf(&output, "- %s\n", boundary)
	}
	fmt.Fprintln(&output, "\nOptional examples:")
	for _, example := range instructions.Examples {
		fmt.Fprintf(&output, "- %s\n", example)
	}
	fmt.Fprintln(&output, "\nAvailable commands:")
	for _, command := range instructions.Commands {
		fmt.Fprintf(&output, "- %s\n", command)
	}
	return output.String()
}

// JSONSchema is the subset of JSON Schema needed by generated MCP and OpenAPI
// input descriptions.
type JSONSchema struct {
	Type                 string                `json:"type"`
	Description          string                `json:"description,omitempty"`
	Format               string                `json:"format,omitempty"`
	Enum                 []string              `json:"enum,omitempty"`
	Properties           map[string]JSONSchema `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	AdditionalProperties *bool                 `json:"additionalProperties,omitempty"`
}

// MCPToolDescription is an SDK-neutral MCP tool descriptor.
type MCPToolDescription struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema JSONSchema `json:"inputSchema"`
	Annotations struct {
		ReadOnlyHint bool `json:"readOnlyHint"`
	} `json:"annotations"`
}

// MCPTools generates tool descriptions for operations intended for MCP.
func MCPTools() []MCPToolDescription {
	tools := make([]MCPToolDescription, 0, len(registry))
	for _, operation := range Operations() {
		if operation.MCPName == "" {
			continue
		}
		tool := MCPToolDescription{
			Name:        operation.MCPName,
			Description: operation.Summary + ". " + operation.Description,
			InputSchema: schemaForOperation(operation),
		}
		tool.Annotations.ReadOnlyHint = !operation.Mutating
		tools = append(tools, tool)
	}
	return tools
}

// MCPToolsJSON renders the generated SDK-neutral MCP tool descriptions.
func MCPToolsJSON() ([]byte, error) {
	return marshalDocument(MCPTools())
}

func schemaForOperation(operation Operation) JSONSchema {
	additionalProperties := false
	schema := JSONSchema{
		Type:                 "object",
		Properties:           make(map[string]JSONSchema, len(operation.Parameters)+2),
		AdditionalProperties: &additionalProperties,
	}
	for _, parameter := range operation.Parameters {
		schema.Properties[parameter.Name] = JSONSchema{
			Type:        parameter.Type,
			Description: parameter.Description,
			Format:      parameter.Format,
			Enum:        append([]string(nil), parameter.Enum...),
		}
		if parameter.Required {
			schema.Required = append(schema.Required, parameter.Name)
		}
	}
	if operation.Mutating {
		schema.Properties["client_id"] = JSONSchema{Type: "string", Description: "Stable client identity used with request_id for idempotency."}
		schema.Properties["request_id"] = JSONSchema{Type: "string", Description: "Client-chosen idempotency key; must accompany client_id."}
	}
	return schema
}

func marshalDocument(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode generated document: %w", err)
	}
	return output.Bytes(), nil
}
