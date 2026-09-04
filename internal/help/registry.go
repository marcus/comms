// Package help owns Comms' transport-neutral capability registry and the
// human- and machine-readable projections generated from it.
package help

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	// CapabilitySchema identifies the compatibility contract for capability
	// descriptions. Additive fields may be introduced without changing it.
	CapabilitySchema = "comms.capabilities.v1"
	// InstructionsSchema identifies the compatibility contract for structured
	// agent instructions.
	InstructionsSchema = "comms.instructions.v1"
	// ResponseSchema identifies the versioned HTTP success envelope.
	ResponseSchema = "comms.response.v1"
	// ProtocolVersion is the HTTP/RPC protocol described by this registry.
	ProtocolVersion = "v1"
)

// ParameterLocation describes where an HTTP adapter carries an operation
// input. MCP inputs are always exposed as one object regardless of location.
type ParameterLocation string

const (
	PathParameter  ParameterLocation = "path"
	QueryParameter ParameterLocation = "query"
	BodyParameter  ParameterLocation = "body"
)

// Parameter describes one transport-neutral operation input and its HTTP
// projection.
type Parameter struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Type        string            `json:"type"`
	Format      string            `json:"format,omitempty"`
	Location    ParameterLocation `json:"location"`
	Required    bool              `json:"required,omitempty"`
	Enum        []string          `json:"enum,omitempty"`
}

// HTTPBinding maps an operation onto the versioned service API.
type HTTPBinding struct {
	Method string `json:"method"`
	Path   string `json:"path"`
}

// Operation is the authored unit in the registry. Adapters project this
// metadata into their own discovery formats; they do not maintain parallel
// command or tool catalogs.
type Operation struct {
	ID               string       `json:"id"`
	Category         string       `json:"category"`
	Summary          string       `json:"summary"`
	Description      string       `json:"description"`
	CLI              string       `json:"cli,omitempty"`
	HTTP             *HTTPBinding `json:"http,omitempty"`
	MCPName          string       `json:"mcp_tool,omitempty"`
	Mutating         bool         `json:"mutating,omitempty"`
	RequiresIdentity bool         `json:"requires_identity,omitempty"`
	UnixOnly         bool         `json:"unix_only,omitempty"`
	Parameters       []Parameter  `json:"parameters,omitempty"`
}

func p(name, description, typ string, location ParameterLocation, required bool) Parameter {
	return Parameter{Name: name, Description: description, Type: typ, Location: location, Required: required}
}

func route(method, path string) *HTTPBinding { return &HTTPBinding{Method: method, Path: path} }

// registry is the single authored catalog. CLI usage, capability JSON,
// OpenAPI, MCP tools, and agent instructions are all generated from it.
var registry = []Operation{
	{ID: "service.serve", Category: "Service", Summary: "Run the local Comms service", Description: "Runs the foreground process that exclusively owns the SQLite store.", CLI: "serve [--socket PATH] [--db PATH] [--listen ADDRESS]"},
	{ID: "service.shutdown", Category: "Service", Summary: "Shut down a matching service incarnation", Description: "Requests a graceful stop of the Unix-socket service identified by server_instance_id. The route is not exposed by a TCP listener. Shutdown begins after the response is committed.", HTTP: route("POST", "/v1/admin/shutdown"), UnixOnly: true, Parameters: []Parameter{
		p("server_instance_id", "Expected process incarnation from GET /v1/hello.", "string", BodyParameter, true),
	}},
	{ID: "store.handshake", Category: "Service", Summary: "Inspect the server handshake", Description: "Returns store, protocol, schema, server version, process incarnation, launch mode, and capability information.", CLI: "hello", HTTP: route("GET", "/v1/hello"), MCPName: "comms_handshake"},
	{ID: "capability.describe", Category: "Service", Summary: "Describe supported capabilities", Description: "Returns this versioned operation catalog for client feature discovery.", CLI: "capabilities", HTTP: route("GET", "/v1/capabilities"), MCPName: "comms_describe_capabilities"},
	{ID: "api.openapi", Category: "Service", Summary: "Render the HTTP OpenAPI contract", Description: "Returns the generated OpenAPI 3.1 contract for the versioned HTTP interface.", CLI: "openapi", HTTP: route("GET", "/v1/openapi.json")},
	{ID: "instructions.generate", Category: "Service", Summary: "Show agent instructions", Description: "Describes Comms capabilities, guarantees, boundaries, and optional usage examples.", CLI: "instructions", HTTP: route("GET", "/v1/instructions"), MCPName: "comms_instructions"},
	{ID: "health.get", Category: "Service", Summary: "Check service health", Description: "Returns a lightweight service liveness result.", CLI: "health", HTTP: route("GET", "/v1/health"), MCPName: "comms_health"},
	{ID: "doctor.run", Category: "Service", Summary: "Diagnose the service and store", Description: "Runs non-destructive operational checks and reports actionable findings.", CLI: "doctor", HTTP: route("GET", "/v1/doctor"), MCPName: "comms_doctor"},
	{ID: "version.get", Category: "Service", Summary: "Show the client version", Description: "Returns build version and commit information without requiring the service.", CLI: "version"},

	{ID: "agent.join", Category: "Agents", Summary: "Join or reconnect a session", Description: "Creates an addressable logical session, or reconnects one by external reference.", CLI: "join [HANDLE] [--display-name TEXT] [--purpose TEXT] [--harness NAME] [--project NAME] [--session-ref REF] [--external-namespace NAME --external-key KEY] [--context PATH]", HTTP: route("POST", "/v1/agents/join"), MCPName: "comms_join", Mutating: true, Parameters: []Parameter{
		p("handle", "Friendly, case-insensitively unique session handle.", "string", BodyParameter, false),
		p("display_name", "Optional human-readable session name.", "string", BodyParameter, false),
		p("purpose", "Optional description of the session's purpose.", "string", BodyParameter, false),
		p("harness", "Optional harness name.", "string", BodyParameter, false),
		p("project", "Optional project description.", "string", BodyParameter, false),
		p("session_ref", "Optional harness-owned session reference.", "string", BodyParameter, false),
		p("external_namespace", "Integration namespace; must accompany external_key.", "string", BodyParameter, false),
		p("external_key", "Integration-owned key; must accompany external_namespace.", "string", BodyParameter, false),
	}},
	{ID: "agent.whoami", Category: "Agents", Summary: "Resolve the active session identity", Description: "Returns the selected agent and the source used to select it.", CLI: "whoami", HTTP: route("GET", "/v1/whoami"), MCPName: "comms_whoami", RequiresIdentity: true},
	{ID: "agent.get", Category: "Agents", Summary: "Get an agent", Description: "Returns an active or retired agent by stable ID or friendly handle.", CLI: "agent get AGENT", HTTP: route("GET", "/v1/agents/{agent}"), MCPName: "comms_get_agent", Parameters: []Parameter{p("agent", "Stable agent ID or current friendly handle.", "string", PathParameter, true)}},
	{ID: "agent.update", Category: "Agents", Summary: "Update an agent", Description: "Updates mutable presentation and session metadata without changing stable identity.", CLI: "agent update AGENT [--handle HANDLE] [--display-name TEXT] [--purpose TEXT] [--harness NAME] [--project NAME] [--session-ref REF]", HTTP: route("PATCH", "/v1/agents/{agent}"), MCPName: "comms_update_agent", Mutating: true, Parameters: []Parameter{
		p("agent", "Stable agent ID or current friendly handle.", "string", PathParameter, true),
		p("handle", "New friendly handle.", "string", BodyParameter, false),
		p("display_name", "New display name.", "string", BodyParameter, false),
		p("purpose", "New purpose.", "string", BodyParameter, false),
		p("harness", "New harness description.", "string", BodyParameter, false),
		p("project", "New project description.", "string", BodyParameter, false),
		p("session_ref", "New harness-owned session reference.", "string", BodyParameter, false),
	}},
	{ID: "agent.retire", Category: "Agents", Summary: "Retire an agent", Description: "Marks a session endpoint inactive while preserving its authored messages and subscriptions.", CLI: "agent retire AGENT", HTTP: route("POST", "/v1/agents/{agent}/retire"), MCPName: "comms_retire_agent", Mutating: true, Parameters: []Parameter{p("agent", "Stable agent ID or current friendly handle.", "string", PathParameter, true)}},
	{ID: "agent.list", Category: "Agents", Summary: "List discoverable agents", Description: "Lists active agents in deterministic order with bounded pagination.", CLI: "agents [--limit N] [--cursor CURSOR]", HTTP: route("GET", "/v1/agents"), MCPName: "comms_list_agents", Parameters: pagingParameters()},

	{ID: "topic.create", Category: "Topics and subscriptions", Summary: "Create a public topic", Description: "Creates a named, discoverable public topic.", CLI: "topic create NAME [--description TEXT]", HTTP: route("POST", "/v1/topics"), MCPName: "comms_create_topic", Mutating: true, Parameters: []Parameter{
		p("name", "Friendly, case-insensitively unique topic name.", "string", BodyParameter, true),
		p("description", "Optional topic description.", "string", BodyParameter, false),
	}},
	{ID: "topic.ensure", Category: "Topics and subscriptions", Summary: "Ensure an integration topic", Description: "Finds or creates the one topic associated with an integration-owned external reference.", CLI: "topic ensure --external-namespace NAME --external-key KEY --name NAME [--description TEXT]", HTTP: route("PUT", "/v1/topics/by-external-reference"), MCPName: "comms_ensure_topic", Mutating: true, Parameters: []Parameter{
		p("external_namespace", "Integration namespace.", "string", BodyParameter, true),
		p("external_key", "Integration-owned key.", "string", BodyParameter, true),
		p("name", "Preferred display name.", "string", BodyParameter, true),
		p("description", "Optional topic description.", "string", BodyParameter, false),
	}},
	{ID: "topic.update", Category: "Topics and subscriptions", Summary: "Update a topic", Description: "Updates a topic's mutable name or description without changing its stable or external identity.", CLI: "topic update TOPIC [--name NAME] [--description TEXT]", HTTP: route("PATCH", "/v1/topics/{topic}"), MCPName: "comms_update_topic", Mutating: true, Parameters: []Parameter{
		p("topic", "Stable topic ID or current name.", "string", PathParameter, true),
		p("name", "New friendly topic name.", "string", BodyParameter, false),
		p("description", "New topic description.", "string", BodyParameter, false),
	}},
	{ID: "topic.archive", Category: "Topics and subscriptions", Summary: "Archive a topic", Description: "Hides a topic from ordinary discovery while preserving its records.", CLI: "topic archive TOPIC", HTTP: route("POST", "/v1/topics/{topic}/archive"), MCPName: "comms_archive_topic", Mutating: true, Parameters: []Parameter{p("topic", "Stable topic ID or current name.", "string", PathParameter, true)}},
	{ID: "topic.list", Category: "Topics and subscriptions", Summary: "List public topics", Description: "Lists discoverable public topics; direct topics are omitted.", CLI: "topics [--limit N] [--cursor CURSOR]", HTTP: route("GET", "/v1/topics"), MCPName: "comms_list_topics", Parameters: pagingParameters()},
	{ID: "subscription.follow", Category: "Topics and subscriptions", Summary: "Follow a public topic", Description: "Creates or resumes the selected agent's subscription and cursor.", CLI: "topic follow TOPIC", HTTP: route("PUT", "/v1/topics/{topic}/subscription"), MCPName: "comms_follow_topic", Mutating: true, RequiresIdentity: true, Parameters: []Parameter{p("topic", "Stable topic ID or current name.", "string", PathParameter, true)}},
	{ID: "subscription.unfollow", Category: "Topics and subscriptions", Summary: "Unfollow a public topic", Description: "Stops ordinary inbox routing while preserving the subscription cursor.", CLI: "topic unfollow TOPIC", HTTP: route("DELETE", "/v1/topics/{topic}/subscription"), MCPName: "comms_unfollow_topic", Mutating: true, RequiresIdentity: true, Parameters: []Parameter{p("topic", "Stable topic ID or current name.", "string", PathParameter, true)}},
	{ID: "subscription.list", Category: "Topics and subscriptions", Summary: "List subscriptions", Description: "Lists the selected agent's active and optionally former subscriptions.", CLI: "subscriptions [--all] [--limit N] [--cursor CURSOR]", HTTP: route("GET", "/v1/subscriptions"), MCPName: "comms_list_subscriptions", RequiresIdentity: true, Parameters: append([]Parameter{p("all", "Include unfollowed subscriptions.", "boolean", QueryParameter, false)}, pagingParameters()...)},

	{ID: "message.publish", Category: "Messages", Summary: "Publish a topic message", Description: "Appends a titled root message to one topic.", CLI: "publish TOPIC --title TEXT [--body TEXT | --body-file PATH | -] [--expires-at TIME | --expires-in DURATION | --never-expires] [--metadata-json JSON]", HTTP: route("POST", "/v1/messages"), MCPName: "comms_publish", Mutating: true, RequiresIdentity: true, Parameters: messageParameters(true, true)},
	{ID: "message.direct_send", Category: "Messages", Summary: "Send a direct message", Description: "Ensures a two-member direct topic and publishes a root message to it.", CLI: "send @AGENT --title TEXT [--body TEXT | --body-file PATH | -] [--expires-at TIME | --expires-in DURATION | --never-expires] [--metadata-json JSON]", HTTP: route("POST", "/v1/direct-messages"), MCPName: "comms_send", Mutating: true, RequiresIdentity: true, Parameters: append([]Parameter{p("agent", "Recipient stable ID or current handle.", "string", BodyParameter, true)}, messageContentParameters(true)...)},
	{ID: "message.reply", Category: "Messages", Summary: "Reply in a thread", Description: "Appends a reply in the same topic and thread as its parent.", CLI: "reply MESSAGE_ID [--title TEXT] [--body TEXT | --body-file PATH | -] [--expires-at TIME | --expires-in DURATION | --never-expires] [--metadata-json JSON]", HTTP: route("POST", "/v1/messages/{message}/replies"), MCPName: "comms_reply", Mutating: true, RequiresIdentity: true, Parameters: append([]Parameter{p("message", "Parent message ID.", "string", PathParameter, true)}, messageContentParameters(false)...)},
	{ID: "message.inbox", Category: "Messages", Summary: "List routed messages", Description: "Lists messages routed to the selected agent without advancing any read cursor.", CLI: "inbox [--unread] [--threads] [--limit N] [--cursor CURSOR]", HTTP: route("GET", "/v1/inbox"), MCPName: "comms_inbox", RequiresIdentity: true, Parameters: append([]Parameter{
		p("unread", "Return only messages beyond their topic read cursor.", "boolean", QueryParameter, false),
		p("threads", "Collapse results to thread summaries.", "boolean", QueryParameter, false),
	}, pagingParameters()...)},
	{ID: "message.topic", Category: "Messages", Summary: "Read a topic", Description: "Lists visible messages in one topic without advancing a cursor.", CLI: "topic messages TOPIC [--limit N] [--cursor CURSOR]", HTTP: route("GET", "/v1/topics/{topic}/messages"), MCPName: "comms_topic_messages", Parameters: append([]Parameter{p("topic", "Stable topic ID or current name.", "string", PathParameter, true)}, pagingParameters()...)},
	{ID: "message.thread", Category: "Messages", Summary: "Read a message thread", Description: "Returns a thread with live descendants and retained expired ancestors needed for context.", CLI: "thread MESSAGE_ID [--limit N] [--cursor CURSOR]", HTTP: route("GET", "/v1/messages/{message}/thread"), MCPName: "comms_thread", Parameters: append([]Parameter{p("message", "Any message ID in the thread.", "string", PathParameter, true)}, pagingParameters()...)},
	{ID: "message.peek", Category: "Messages", Summary: "Inspect one message", Description: "Returns a message without advancing a read cursor.", CLI: "peek MESSAGE_ID", HTTP: route("GET", "/v1/messages/{message}"), MCPName: "comms_peek", Parameters: []Parameter{p("message", "Message ID.", "string", PathParameter, true)}},
	{ID: "message.read_through", Category: "Messages", Summary: "Acknowledge through a message", Description: "Advances the selected agent's topic cursor through the message sequence, acknowledging all earlier visible messages.", CLI: "read-through MESSAGE_ID", HTTP: route("POST", "/v1/messages/{message}/read-through"), MCPName: "comms_read_through", Mutating: true, RequiresIdentity: true, Parameters: []Parameter{p("message", "Message ID whose sequence becomes the new cursor floor.", "string", PathParameter, true)}},
	{ID: "message.receipts", Category: "Messages", Summary: "Inspect cursor-derived receipts", Description: "Reports which relevant subscribers have explicitly advanced their cursor through a message.", CLI: "receipts MESSAGE_ID", HTTP: route("GET", "/v1/messages/{message}/receipts"), MCPName: "comms_receipts", Parameters: []Parameter{p("message", "Message ID.", "string", PathParameter, true)}},
	{ID: "message.search", Category: "Messages", Summary: "Search messages", Description: "Searches visible message titles and bodies without changing any cursor.", CLI: "search QUERY [--from AGENT] [--topic TOPIC] [--limit N] [--cursor CURSOR]", HTTP: route("GET", "/v1/search"), MCPName: "comms_search", Parameters: append([]Parameter{
		p("query", "Text query.", "string", QueryParameter, true),
		p("from", "Optional author ID or handle filter.", "string", QueryParameter, false),
		p("topic", "Optional topic ID or name filter.", "string", QueryParameter, false),
	}, pagingParameters()...)},
	{ID: "message.observe", Category: "Messages", Summary: "Observe all traffic", Description: "Lists public and direct traffic for trusted-local inspection without changing any cursor.", CLI: "observe [--limit N] [--cursor CURSOR]", HTTP: route("GET", "/v1/observe"), MCPName: "comms_observe", Parameters: pagingParameters()},

	{ID: "retention.status", Category: "Maintenance", Summary: "Inspect retention state", Description: "Reports expiration and purge status without changing stored data.", CLI: "retention status", HTTP: route("GET", "/v1/retention"), MCPName: "comms_retention_status"},
	{ID: "retention.purge", Category: "Maintenance", Summary: "Purge eligible expired messages", Description: "Removes expired messages while retaining ancestors required by live descendants.", CLI: "purge [--dry-run]", HTTP: route("POST", "/v1/purge"), MCPName: "comms_purge", Mutating: true, Parameters: []Parameter{p("dry_run", "Report eligible removals without deleting them.", "boolean", BodyParameter, false)}},
	{ID: "diagnostic.export", Category: "Maintenance", Summary: "Export diagnostic JSONL", Description: "Streams deterministic, versioned inspection records; it is not a backup, replication, or import contract.", CLI: "export [--output PATH]", HTTP: route("GET", "/v1/export"), MCPName: "comms_export"},
}

func pagingParameters() []Parameter {
	return []Parameter{
		p("limit", "Maximum number of results to return.", "integer", QueryParameter, false),
		p("cursor", "Opaque continuation cursor, distinct from a subscription read cursor.", "string", QueryParameter, false),
	}
}

func messageContentParameters(titleRequired bool) []Parameter {
	return []Parameter{
		p("title", "Message title; replies may omit it and inherit the root title for presentation.", "string", BodyParameter, titleRequired),
		p("body", "UTF-8 message body, conventionally Markdown.", "string", BodyParameter, true),
		p("expires_at", "Explicit expiration instant; mutually exclusive with expires_in and never_expires.", "string", BodyParameter, false),
		p("expires_in", "Expiration duration; mutually exclusive with expires_at and never_expires.", "string", BodyParameter, false),
		p("never_expires", "Disable expiration for this message.", "boolean", BodyParameter, false),
		p("metadata", "Optional namespaced metadata object.", "object", BodyParameter, false),
	}
}

func messageParameters(topic, titleRequired bool) []Parameter {
	parameters := make([]Parameter, 0, 1+len(messageContentParameters(titleRequired)))
	if topic {
		parameters = append(parameters, p("topic", "Stable topic ID or current name.", "string", BodyParameter, true))
	}
	return append(parameters, messageContentParameters(titleRequired)...)
}

// Operations returns a defensive copy of the ordered operation registry.
func Operations() []Operation {
	operations := make([]Operation, len(registry))
	for i, operation := range registry {
		operations[i] = operation
		operations[i].Parameters = append([]Parameter(nil), operation.Parameters...)
		for j := range operations[i].Parameters {
			operations[i].Parameters[j].Enum = append([]string(nil), operation.Parameters[j].Enum...)
		}
		if operation.HTTP != nil {
			binding := *operation.HTTP
			operations[i].HTTP = &binding
		}
	}
	return operations
}

var pathPlaceholder = regexp.MustCompile(`\{([^}]+)\}`)

// Validate reports catalog mistakes that would make generated surfaces drift or
// become ambiguous.
func Validate() error {
	ids := make(map[string]struct{}, len(registry))
	commands := make(map[string]string, len(registry))
	routes := make(map[string]string, len(registry))
	tools := make(map[string]string, len(registry))
	for _, operation := range registry {
		if operation.ID == "" || operation.Category == "" || operation.Summary == "" || operation.Description == "" {
			return fmt.Errorf("operation %q has incomplete descriptive metadata", operation.ID)
		}
		if _, exists := ids[operation.ID]; exists {
			return fmt.Errorf("duplicate operation id %q", operation.ID)
		}
		ids[operation.ID] = struct{}{}
		if operation.CLI != "" {
			command := strings.Fields(operation.CLI)[0]
			if previous, exists := commands[operation.CLI]; exists {
				return fmt.Errorf("operations %q and %q have duplicate CLI synopsis %q", previous, operation.ID, operation.CLI)
			}
			if strings.HasPrefix(command, "-") {
				return fmt.Errorf("operation %q has invalid CLI command %q", operation.ID, command)
			}
			commands[operation.CLI] = operation.ID
		}
		if operation.HTTP != nil {
			routeKey := strings.ToUpper(operation.HTTP.Method) + " " + operation.HTTP.Path
			if previous, exists := routes[routeKey]; exists {
				return fmt.Errorf("operations %q and %q have duplicate HTTP route %q", previous, operation.ID, routeKey)
			}
			routes[routeKey] = operation.ID
			pathParameters := make(map[string]bool)
			for _, parameter := range operation.Parameters {
				if parameter.Location == PathParameter {
					pathParameters[parameter.Name] = parameter.Required
				}
			}
			for _, match := range pathPlaceholder.FindAllStringSubmatch(operation.HTTP.Path, -1) {
				if !pathParameters[match[1]] {
					return fmt.Errorf("operation %q path placeholder %q lacks a required path parameter", operation.ID, match[1])
				}
				delete(pathParameters, match[1])
			}
			if len(pathParameters) != 0 {
				names := make([]string, 0, len(pathParameters))
				for name := range pathParameters {
					names = append(names, name)
				}
				sort.Strings(names)
				return fmt.Errorf("operation %q has path parameters absent from route: %s", operation.ID, strings.Join(names, ", "))
			}
		}
		if operation.MCPName != "" {
			if previous, exists := tools[operation.MCPName]; exists {
				return fmt.Errorf("operations %q and %q have duplicate MCP tool %q", previous, operation.ID, operation.MCPName)
			}
			tools[operation.MCPName] = operation.ID
		}
		parameterNames := make(map[string]struct{}, len(operation.Parameters))
		for _, parameter := range operation.Parameters {
			if _, exists := parameterNames[parameter.Name]; exists {
				return fmt.Errorf("operation %q has duplicate parameter %q", operation.ID, parameter.Name)
			}
			parameterNames[parameter.Name] = struct{}{}
		}
	}
	return nil
}
