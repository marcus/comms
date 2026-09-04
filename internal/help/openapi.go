package help

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
)

// OpenAPIDocument generates the HTTP contract from the operation registry.
// The returned representation can be encoded as JSON or YAML by adapters.
func OpenAPIDocument() map[string]any {
	paths := make(map[string]any)
	for _, operation := range Operations() {
		if operation.HTTP == nil {
			continue
		}
		pathItem, _ := paths[operation.HTTP.Path].(map[string]any)
		if pathItem == nil {
			pathItem = make(map[string]any)
			paths[operation.HTTP.Path] = pathItem
		}
		pathItem[strings.ToLower(operation.HTTP.Method)] = openAPIOperation(operation)
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":       "Comms API",
			"version":     ProtocolVersion,
			"description": "Versioned transport for the Comms application operations. Unix-socket HTTP is the default local binding; TCP exposure defaults to loopback.",
		},
		"servers": []any{map[string]any{"url": "http://localhost", "description": "Local Comms service (normally reached over its Unix socket)"}},
		"paths":   paths,
		"components": map[string]any{
			"parameters": map[string]any{
				"AgentIdentity": map[string]any{
					"name":        "X-Comms-Agent-ID",
					"in":          "header",
					"required":    true,
					"description": "Stable agent ID selected by the client; this controls routing and attribution, not authentication.",
					"schema":      map[string]any{"type": "string"},
				},
			},
			"schemas": map[string]any{
				"Error": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             []string{"error"},
					"properties": map[string]any{
						"error": map[string]any{
							"type":                 "object",
							"additionalProperties": false,
							"required":             []string{"code", "message", "details"},
							"properties": map[string]any{
								"code":    map[string]any{"type": "string", "description": "Stable machine-readable error code."},
								"message": map[string]any{"type": "string", "description": "Human-readable error text; clients must not branch on it."},
								"details": map[string]any{"type": "object", "additionalProperties": true},
							},
						},
					},
				},
			},
		},
	}
}

// OpenAPIJSON renders the generated OpenAPI 3.1 document as deterministic,
// indented JSON, which is also valid YAML 1.2.
func OpenAPIJSON() ([]byte, error) {
	return marshalDocument(OpenAPIDocument())
}

func openAPIOperation(operation Operation) map[string]any {
	document := map[string]any{
		"operationId": operation.ID,
		"summary":     operation.Summary,
		"description": operation.Description,
		"tags":        []string{operation.Category},
		"responses": map[string]any{
			"200": map[string]any{
				"description": "Successful " + operation.ID + " response.",
				"content":     successContent(operation),
			},
			"default": map[string]any{
				"description": "Stable Comms error envelope.",
				"content": map[string]any{
					"application/json": map[string]any{"schema": map[string]any{"$ref": "#/components/schemas/Error"}},
				},
			},
		},
	}
	parameters := make([]any, 0)
	if operation.RequiresIdentity {
		parameters = append(parameters, map[string]any{"$ref": "#/components/parameters/AgentIdentity"})
	}
	bodyParameters := make([]Parameter, 0)
	for _, parameter := range operation.Parameters {
		if parameter.Location == BodyParameter {
			bodyParameters = append(bodyParameters, parameter)
			continue
		}
		parameters = append(parameters, map[string]any{
			"name":        parameter.Name,
			"in":          string(parameter.Location),
			"required":    parameter.Required,
			"description": parameter.Description,
			"schema":      openAPISchema(parameter),
		})
	}
	if len(parameters) != 0 {
		document["parameters"] = parameters
	}
	if len(bodyParameters) != 0 || operation.Mutating {
		required := make([]string, 0)
		properties := make(map[string]any, len(bodyParameters)+2)
		for _, parameter := range bodyParameters {
			properties[parameter.Name] = openAPISchema(parameter)
			if parameter.Required {
				required = append(required, parameter.Name)
			}
		}
		if operation.Mutating {
			properties["client_id"] = map[string]any{"type": "string", "description": "Stable client identity used with request_id for idempotency."}
			properties["request_id"] = map[string]any{"type": "string", "description": "Client-chosen idempotency key; must accompany client_id."}
		}
		schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
		if len(required) != 0 {
			schema["required"] = required
		}
		document["requestBody"] = map[string]any{
			"required": len(required) != 0,
			"content":  map[string]any{"application/json": map[string]any{"schema": schema}},
		}
	}
	return document
}

func successContent(operation Operation) map[string]any {
	if operation.ID == "diagnostic.export" {
		return map[string]any{"application/x-ndjson": map[string]any{"schema": map[string]any{
			"type": "string", "description": "A stream of comms.export.v1 JSON objects, one per line.",
		}}}
	}
	dataType := responseType(operation.ID)
	dataSchema := map[string]any{"type": "object", "additionalProperties": true}
	if dataType != nil {
		dataSchema = schemaFromType(dataType, map[reflect.Type]bool{})
	}
	return map[string]any{"application/json": map[string]any{"schema": map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"schema", "data"},
		"properties": map[string]any{
			"schema": map[string]any{"type": "string", "const": ResponseSchema},
			"data":   dataSchema,
		},
	}}}
}

type whoamiResponse struct {
	Agent  domain.Agent `json:"agent"`
	Source string       `json:"source"`
}

func responseType(id string) reflect.Type {
	var value any
	switch id {
	case "store.handshake":
		value = app.Handshake{}
	case "capability.describe":
		value = CapabilityDescription{}
	case "api.openapi":
		value = map[string]any{}
	case "instructions.generate":
		value = Instructions{}
	case "health.get":
		value = map[string]string{}
	case "doctor.run":
		value = app.DoctorReport{}
	case "agent.join":
		value = app.JoinResponse{}
	case "agent.whoami":
		value = whoamiResponse{}
	case "agent.get", "agent.update", "agent.retire":
		value = domain.Agent{}
	case "agent.list":
		value = app.Page[domain.Agent]{}
	case "topic.create", "topic.update", "topic.archive":
		value = domain.Topic{}
	case "topic.ensure":
		value = app.EnsureTopicResponse{}
	case "topic.list":
		value = app.Page[domain.Topic]{}
	case "subscription.follow", "subscription.unfollow":
		value = domain.Subscription{}
	case "subscription.list":
		value = app.Page[domain.Subscription]{}
	case "message.publish", "message.direct_send", "message.reply", "message.peek":
		value = domain.Message{}
	case "message.inbox", "message.topic", "message.thread", "message.search", "message.observe":
		value = app.Page[domain.Message]{}
	case "message.read_through":
		value = app.ReadThroughResponse{}
	case "message.receipts":
		value = []app.Receipt{}
	case "retention.status":
		value = app.RetentionStatus{}
	case "retention.purge":
		value = app.PurgeRun{}
	default:
		return nil
	}
	return reflect.TypeOf(value)
}

var timeType = reflect.TypeOf(time.Time{})
var rawMessageType = reflect.TypeOf(json.RawMessage{})

func schemaFromType(t reflect.Type, active map[reflect.Type]bool) map[string]any {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if t == rawMessageType {
		return map[string]any{"type": "object", "additionalProperties": true}
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		return map[string]any{"type": "array", "items": schemaFromType(t.Elem(), active)}
	case reflect.Map, reflect.Interface:
		return map[string]any{"type": "object", "additionalProperties": true}
	case reflect.Struct:
		if active[t] {
			return map[string]any{"type": "object"}
		}
		active[t] = true
		defer delete(active, t)
		properties := map[string]any{}
		required := []string{}
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}
			tag := field.Tag.Get("json")
			if tag == "-" {
				continue
			}
			parts := strings.Split(tag, ",")
			name := parts[0]
			if name == "" {
				name = field.Name
			}
			omitempty := false
			for _, part := range parts[1:] {
				if part == "omitempty" {
					omitempty = true
				}
			}
			if field.Anonymous && parts[0] == "" {
				embedded := schemaFromType(field.Type, active)
				if values, ok := embedded["properties"].(map[string]any); ok {
					for key, value := range values {
						properties[key] = value
					}
				}
				if values, ok := embedded["required"].([]string); ok {
					required = append(required, values...)
				}
				continue
			}
			properties[name] = schemaFromType(field.Type, active)
			if !omitempty && field.Type.Kind() != reflect.Pointer {
				required = append(required, name)
			}
		}
		schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	default:
		return map[string]any{}
	}
}

func openAPISchema(parameter Parameter) map[string]any {
	schema := map[string]any{"type": parameter.Type}
	if parameter.Description != "" {
		schema["description"] = parameter.Description
	}
	if parameter.Format != "" {
		schema["format"] = parameter.Format
	}
	if len(parameter.Enum) != 0 {
		schema["enum"] = append([]string(nil), parameter.Enum...)
	}
	if parameter.Type == "integer" {
		schema["minimum"] = 1
	}
	return schema
}

// HTTPRouteKey returns the stable method-and-path identity used by adapters.
func HTTPRouteKey(operation Operation) string {
	if operation.HTTP == nil {
		return ""
	}
	return fmt.Sprintf("%s %s", strings.ToUpper(operation.HTTP.Method), operation.HTTP.Path)
}
