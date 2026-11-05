package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

type Spec struct {
	Schemas    map[string]SchemaNode
	Operations []Operation
}

type SchemaNode struct {
	Description string                `json:"description"`
	Type        json.RawMessage       `json:"type"`
	Format      string                `json:"format"`
	Properties  map[string]SchemaNode `json:"properties"`
	OneOf       []SchemaNode          `json:"oneOf"`
	AnyOf       []SchemaNode          `json:"anyOf"`
	AllOf       []SchemaNode          `json:"allOf"`
	Enum        []string              `json:"enum"`
}

type Operation struct {
	ID         string
	Parameters []Parameter
}

type Parameter struct {
	Name        string
	In          string
	Description string
	Format      string
}

type specDocument struct {
	Components struct {
		Schemas map[string]SchemaNode `json:"schemas"`
	} `json:"components"`
	Paths map[string]map[string]json.RawMessage `json:"paths"`
}

type operationDocument struct {
	OperationID string `json:"operationId"`
	Parameters  []struct {
		Name        string     `json:"name"`
		In          string     `json:"in"`
		Description string     `json:"description"`
		Schema      SchemaNode `json:"schema"`
	} `json:"parameters"`
}

func loadSpec(path string) (Spec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Spec{}, fmt.Errorf("read spec %q: %w", path, err)
	}
	var document specDocument
	if err := json.Unmarshal(data, &document); err != nil {
		return Spec{}, fmt.Errorf("decode spec %q: %w", path, err)
	}
	operationCapacity := 0
	for _, pathItem := range document.Paths {
		operationCapacity += len(pathItem)
	}
	result := Spec{
		Schemas:    document.Components.Schemas,
		Operations: make([]Operation, 0, operationCapacity),
	}
	for path, methods := range document.Paths {
		methodNames := make([]string, 0, len(methods))
		for method := range methods {
			methodNames = append(methodNames, method)
		}
		sort.Strings(methodNames)
		for _, method := range methodNames {
			if !isOperationMethod(method) {
				continue
			}
			var operationDoc operationDocument
			if err := json.Unmarshal(methods[method], &operationDoc); err != nil {
				return Spec{}, fmt.Errorf("decode operation %s %s: %w", method, path, err)
			}
			if operationDoc.OperationID == "" {
				continue
			}
			op := Operation{
				ID:         operationDoc.OperationID,
				Parameters: make([]Parameter, 0, len(operationDoc.Parameters)),
			}
			for _, parameter := range operationDoc.Parameters {
				op.Parameters = append(op.Parameters, Parameter{
					Name:        parameter.Name,
					In:          parameter.In,
					Description: parameter.Description,
					Format:      parameter.Schema.Format,
				})
			}
			result.Operations = append(result.Operations, op)
		}
	}
	sort.Slice(result.Operations, func(i, j int) bool {
		return result.Operations[i].ID < result.Operations[j].ID
	})
	return result, nil
}

func isOperationMethod(method string) bool {
	switch method {
	case "get", "post", "put", "patch", "delete", "head", "options", "trace":
		return true
	default:
		return false
	}
}

func (n SchemaNode) propertyDescription() string {
	if n.Description != "" {
		return n.Description
	}
	for _, branches := range [][]SchemaNode{n.OneOf, n.AnyOf} {
		for _, branch := range branches {
			if branch.Description != "" {
				return branch.Description
			}
		}
	}
	return ""
}

func (n SchemaNode) properties() map[string]SchemaNode {
	result := make(map[string]SchemaNode)
	for _, branches := range [][]SchemaNode{n.OneOf, n.AnyOf, n.AllOf} {
		for _, branch := range branches {
			for name, property := range branch.properties() {
				result[name] = property
			}
		}
	}
	for name, property := range n.Properties {
		result[name] = property
	}
	return result
}
