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
	fmt.Fprintln(&output, "\nEvery response supports --json. Ordinary commands start the local service if needed. status, health, hello, doctor, and stop do not. Use `comms serve` for a foreground process or `brew services start comms` for login startup.")
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
			"Inbox, peek, thread, search, receipts, observe, and both wait operations do not advance read cursors.",
			"The inbox excludes your own messages by default so it shows incoming work; --include-self restores them, and every other surface always retains them.",
			"Waiting is bounded: it returns a match, times out, or reports cancellation, and never blocks forever.",
			"Read-through advances one topic cursor through the named message and acknowledges all earlier visible sequences.",
			"Stable record IDs do not change when friendly handles or topic names change.",
			"Structured responses, error codes, and cursor meanings follow versioned compatibility contracts.",
		},
		Boundaries: []string{
			"Comms carries transient coordination; durable tasks, decisions, artifacts, and secrets belong in their owning systems.",
			"Delivery does not prove that a session is alive, noticed a message, understood it, or acted on it.",
			"A resolved agent wait proves only that a handle is registered and addressable, not that its process is running, idle, or willing to answer.",
			"Direct topics control routing and ordinary discovery, not trusted-local read access.",
			"Comms does not spawn agents, assign work, execute messages, or replace project documentation and task trackers.",
		},
		Examples: []string{
			"comms join build-agent --harness codex --context ./comms-context.json",
			"printf '%s\\n' 'Review notes are in td-123abc.' | comms publish project-alpha --title 'Review ready' -",
			"comms inbox --unread --json",
			"comms --timeout 30s agent wait @publisher --json && comms send @publisher --title 'Briefing' --body 'Start with td-123abc.'",
			"comms --timeout 2m wait --from @publisher --thread msg_example --json",
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

// CommandHelp generates focused help for a registered CLI command or command group.
func CommandHelp(program string, commandArgs ...string) (string, error) {
	if program == "" {
		program = "comms"
	}
	if len(commandArgs) == 0 {
		return CLIUsage(program), nil
	}

	tokens := make([]string, 0, len(commandArgs))
	for _, arg := range commandArgs {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if arg != "" {
			tokens = append(tokens, arg)
		}
	}
	if len(tokens) == 0 {
		return CLIUsage(program), nil
	}

	operations := Operations()

	// 1. Longest command prefix match against registered operations
	for prefixLen := len(tokens); prefixLen >= 1; prefixLen-- {
		prefix := strings.Join(tokens[:prefixLen], " ")
		for _, op := range operations {
			if op.CLI == "" {
				continue
			}
			opTokens := extractCommandTokens(op.CLI)
			if strings.Join(opTokens, " ") == prefix {
				return renderOperationHelp(program, op), nil
			}
		}
	}

	// 2. Group match (e.g. "topic", "agent", "retention")
	group := tokens[0]
	var groupOps []Operation
	for _, op := range operations {
		if op.CLI == "" {
			continue
		}
		opTokens := extractCommandTokens(op.CLI)
		if len(opTokens) > 1 && opTokens[0] == group {
			groupOps = append(groupOps, op)
		}
	}
	if len(groupOps) > 0 {
		return renderGroupHelp(program, group, groupOps), nil
	}

	return "", fmt.Errorf("unknown command %q; run '%s help'", strings.Join(tokens, " "), program)
}

func extractCommandTokens(cliSynopsis string) []string {
	words := strings.Fields(cliSynopsis)
	tokens := make([]string, 0, len(words))
	for _, w := range words {
		if strings.HasPrefix(w, "-") || strings.HasPrefix(w, "[") {
			break
		}
		if strings.HasPrefix(w, "@") {
			break
		}
		if w == strings.ToUpper(w) && strings.ToLower(w) != strings.ToUpper(w) {
			break
		}
		tokens = append(tokens, w)
	}
	return tokens
}

func renderOperationHelp(program string, op Operation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage:\n  %s %s\n\n", program, op.CLI)
	fmt.Fprintf(&b, "Summary:\n  %s\n\n", op.Summary)
	fmt.Fprintf(&b, "Description:\n  %s\n", op.Description)

	if len(op.Parameters) > 0 {
		fmt.Fprintf(&b, "\nParameters / Flags:\n")
		for _, p := range op.Parameters {
			label := formatParameterLabel(p, op.CLI)
			fmt.Fprintf(&b, "  %-24s %s\n", label, p.Description)
		}
	}

	if op.RequiresIdentity {
		fmt.Fprintf(&b, "\nIdentity:\n  Requires an active agent session (pass --as AGENT or set COMMS_CONTEXT).\n")
	} else {
		fmt.Fprintf(&b, "\nIdentity:\n  CLI-only; does not require an agent identity.\n")
	}

	if op.Mutating {
		fmt.Fprintf(&b, "\nMutation:\n  Mutating operation; creates or modifies local state.\n")
	}

	return b.String()
}

func formatParameterLabel(p Parameter, cliSynopsis string) string {
	if p.Location == PathParameter {
		return strings.ToUpper(p.Name) + " (positional)"
	}
	flagName := "--" + strings.ReplaceAll(p.Name, "_", "-")
	if p.Name == "metadata" && strings.Contains(cliSynopsis, "--metadata-json") {
		return "--metadata-json JSON"
	}
	if p.Type == "boolean" {
		return flagName
	}
	idx := strings.Index(cliSynopsis, flagName+" ")
	if idx != -1 {
		rest := cliSynopsis[idx:]
		fields := strings.Fields(rest)
		if len(fields) >= 2 {
			cleanField := strings.TrimRight(fields[1], "]")
			return flagName + " " + cleanField
		}
	}
	switch p.Type {
	case "integer":
		return flagName + " N"
	case "string":
		if p.Format == "duration" || strings.Contains(p.Name, "duration") || p.Name == "timeout" || p.Name == "expires_in" {
			return flagName + " DURATION"
		}
		if p.Name == "cursor" || p.Name == "after" {
			return flagName + " CURSOR"
		}
		return flagName + " TEXT"
	default:
		return flagName
	}
}

func renderGroupHelp(program string, group string, ops []Operation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Usage:\n  %s %s COMMAND\n\nCommands:\n", program, group)
	for _, op := range ops {
		fmt.Fprintf(&b, "  %-60s %s\n", program+" "+op.CLI, op.Summary)
	}
	fmt.Fprintf(&b, "\nRun '%s <command> --help' for details on a specific command.\n", program)
	return b.String()
}
