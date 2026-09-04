package help

import (
	"fmt"
	"strings"
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
				"content": map[string]any{
					"application/json": map[string]any{"schema": map[string]any{"type": "object"}},
				},
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
