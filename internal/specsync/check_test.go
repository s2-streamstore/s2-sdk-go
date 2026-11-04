package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCheckFindingsAndAcceptedTextStaleness(t *testing.T) {
	root := t.TempDir()
	goDir := filepath.Join(root, "s2")
	if err := os.Mkdir(goDir, 0o755); err != nil {
		t.Fatal(err)
	}
	goSourceText := `package s2

type Widget struct {
	// accepted go
	Accepted string ` + "`json:\"accepted\"`" + `
	// go doc
	Known string ` + "`json:\"known\"`" + `
	Extra string ` + "`json:\"extra\"`" + `
}

type Color string

const ColorRed Color = "red"

const (
	OperationFirst = "first"
	OperationExtra = "extra"
)
`
	if err := os.WriteFile(filepath.Join(goDir, "types.go"), []byte(goSourceText), 0o600); err != nil {
		t.Fatal(err)
	}

	specPath := filepath.Join(root, "openapi.json")
	specText := func(acceptedDescription string) string {
		document := map[string]any{
			"components": map[string]any{
				"schemas": map[string]any{
					"Widget": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"accepted": map[string]any{"type": "string", "description": acceptedDescription},
							"known":    map[string]any{"type": "string", "description": "spec doc"},
							"missing":  map[string]any{"type": "string"},
						},
					},
					"Color": map[string]any{"type": "string", "enum": []string{"red", "blue"}},
					"Operation": map[string]any{
						"type": "string",
						"enum": []string{"first", "missing"},
					},
					"Unmapped": map[string]any{"type": "string"},
				},
			},
			"paths": map[string]any{},
		}
		data, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	writeSpec := func(acceptedDescription string) {
		t.Helper()
		if err := os.WriteFile(specPath, []byte(specText(acceptedDescription)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := Config{
		Spec:         specPath,
		GoPackageDir: goDir,
		UntypedEnums: map[string]string{"Operation": "Operation"},
		Accepted: []Accepted{{
			Kind:   "doc",
			Target: "Widget.Accepted",
			Spec:   "accepted spec",
			Go:     "accepted go",
			Reason: "the SDK keeps a concise wording",
		}},
	}
	loadFindings := func(acceptedDescription string) []Finding {
		t.Helper()
		writeSpec(acceptedDescription)
		spec, err := loadSpec(specPath)
		if err != nil {
			t.Fatal(err)
		}
		goSource, err := loadGoSource(goDir)
		if err != nil {
			t.Fatal(err)
		}
		return check(cfg, spec, goSource)
	}

	initialFindings := loadFindings("accepted spec")
	initial := findingKeys(initialFindings)
	wantInitial := []string{
		"doc:Widget.Known",
		"enum:Color.blue",
		"enum:Operation.extra",
		"enum:Operation.missing",
		"extra-field:Widget.Extra",
		"missing-field:Widget.missing",
		"unmapped-schema:Unmapped",
	}
	if !reflect.DeepEqual(initial, wantInitial) {
		t.Fatalf("initial findings = %v, want %v", initial, wantInitial)
	}
	for target, suffix := range map[string]string{
		"Color.blue":        filepath.Join("s2", "types.go") + ":11:6",
		"Operation.extra":   filepath.Join("s2", "types.go") + ":16:2",
		"Operation.missing": filepath.Join("s2", "types.go") + ":16:2",
	} {
		found := false
		for _, finding := range initialFindings {
			if finding.Kind == "enum" && finding.Target == target {
				found = strings.HasSuffix(finding.Pos, suffix)
				break
			}
		}
		if !found {
			t.Errorf("enum %s position does not end with %q", target, suffix)
		}
	}

	changed := findingKeys(loadFindings("changed spec"))
	wantChanged := append(append([]string(nil), wantInitial...),
		"doc:Widget.Accepted",
		"stale-config:accepted.doc.Widget.Accepted",
	)
	sort.Strings(wantChanged)
	if !reflect.DeepEqual(changed, wantChanged) {
		t.Fatalf("findings after accepted spec text changes = %v, want %v", changed, wantChanged)
	}
}

func findingKeys(findings []Finding) []string {
	if len(findings) == 0 {
		return nil
	}
	keys := make([]string, 0, len(findings))
	for _, finding := range findings {
		keys = append(keys, finding.Kind+":"+finding.Target)
	}
	sort.Strings(keys)
	return keys
}

func runCheck(t *testing.T, spec map[string]any, goSrc string, cfg Config) []string {
	t.Helper()
	return findingKeys(runCheckWithFiles(t, spec, map[string]string{"types.go": goSrc}, cfg))
}

func runCheckWithFiles(t *testing.T, specDocument map[string]any, goFiles map[string]string, cfg Config) []Finding {
	t.Helper()
	cfg, spec, goSource := loadCheckFixture(t, specDocument, goFiles, cfg)
	return check(cfg, spec, goSource)
}

func loadCheckFixture(t *testing.T, specDocument map[string]any, goFiles map[string]string, cfg Config) (Config, Spec, GoSource) {
	t.Helper()
	root := t.TempDir()
	goDir := filepath.Join(root, "s2")
	if err := os.MkdirAll(goDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range sortedKeys(goFiles) {
		path := filepath.Join(goDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(goFiles[name]), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	specPath := filepath.Join(root, "openapi.json")
	data, err := json.Marshal(specDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.Spec = specPath
	cfg.GoPackageDir = goDir
	spec, err := loadSpec(specPath)
	if err != nil {
		t.Fatal(err)
	}
	goSource, err := loadGoSource(goDir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, spec, goSource
}

func apiSpec(schemas, paths map[string]any) map[string]any {
	if schemas == nil {
		schemas = map[string]any{}
	}
	if paths == nil {
		paths = map[string]any{}
	}
	return map[string]any{
		"components": map[string]any{"schemas": schemas},
		"paths":      paths,
	}
}

func objectSchema(description string, properties map[string]any) map[string]any {
	schema := map[string]any{"type": "object"}
	if description != "" {
		schema["description"] = description
	}
	if properties != nil {
		schema["properties"] = properties
	}
	return schema
}

func stringProperty(description string) map[string]any {
	property := map[string]any{"type": "string"}
	if description != "" {
		property["description"] = description
	}
	return property
}

func goFile(declarations ...string) string {
	return "package s2\n\n" + strings.Join(declarations, "\n\n") + "\n"
}

func goStruct(name, description string, fields ...string) string {
	return goComment(description, "") + "type " + name + " struct {\n" + strings.Join(fields, "") + "}\n"
}

func goField(name, fieldType, jsonTag, description string) string {
	field := goComment(description, "\t") + "\t" + name + " " + fieldType
	if jsonTag != "" {
		field += " `json:\"" + jsonTag + "\"`"
	}
	return field + "\n"
}

func goTrailingField(name, fieldType, jsonTag, comment string) string {
	return "\t" + name + " " + fieldType + " `json:\"" + jsonTag + "\"` // " + comment + "\n"
}

func goComment(text, indent string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for index := range lines {
		lines[index] = indent + "// " + lines[index]
	}
	return strings.Join(lines, "\n") + "\n"
}

func operationSpec(operationID string, parameters ...map[string]any) map[string]any {
	items := make([]any, 0, len(parameters))
	for _, parameter := range parameters {
		items = append(items, parameter)
	}
	return apiSpec(nil, map[string]any{
		"/widgets/{widget}": map[string]any{
			"get": map[string]any{
				"operationId": operationID,
				"parameters":  items,
			},
		},
	})
}

func operationParameter(name, location, description string) map[string]any {
	parameter := map[string]any{
		"name":   name,
		"in":     location,
		"schema": map[string]any{"type": "string"},
	}
	if description != "" {
		parameter["description"] = description
	}
	return parameter
}

func TestCheckRules(t *testing.T) {
	cases := []struct {
		name  string
		spec  map[string]any
		goSrc string
		cfg   Config
		want  []string
	}{
		{
			name: "line wrapping normalizes",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": stringProperty("A useful description with several words."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value", "A useful description\nwith several words."))),
		},
		{
			name: "markdown links normalize",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": stringProperty("See [the guide](https://example.com/guide) for details."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value", "See the guide for details."))),
		},
		{
			name: "backticks normalize",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": stringProperty("Write `records` before returning."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value", "Write records before returning."))),
		},
		{
			name: "wording drift",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": stringProperty("Name of the basin."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value", "Name of the stream."))),
			want:  []string{"doc:Thing.Value"},
		},
		{
			name: "missing Go field documentation",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": stringProperty("A documented value."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value", ""))),
			want:  []string{"doc:Thing.Value"},
		},
		{
			name: "missing spec description",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": map[string]any{"type": "string"},
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value", "SDK-only details."))),
		},
		{
			name: "type documentation drift",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("Spec type description.", nil),
			}, nil),
			goSrc: goFile(goStruct("Thing", "Go type description.", "")),
			want:  []string{"doc:Thing"},
		},
		{
			name: "date-time phrases omitted for value and pointer fields",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"at": map[string]any{
						"type":        "string",
						"format":      "date-time",
						"description": "Timestamp in RFC 3339 format.",
					},
					"optional_at": map[string]any{
						"type":        "string",
						"format":      "date-time",
						"description": "Timestamp in RFC 3339 format.",
					},
				}),
			}, nil),
			goSrc: goFile(
				`import "time"`,
				goStruct("Thing", "",
					goField("At", "time.Time", "at", "Timestamp."),
					goField("OptionalAt", "*time.Time", "optional_at", "Timestamp."),
				),
			),
			cfg: Config{FormatPhrases: map[string][]string{
				"date-time": {" in RFC 3339 format"},
			}},
		},
		{
			name: "date-time phrase retained for non-time field",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": map[string]any{
						"type":        "string",
						"format":      "date-time",
						"description": "Timestamp in RFC 3339 format.",
					},
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value", "Timestamp."))),
			cfg: Config{FormatPhrases: map[string][]string{
				"date-time": {" in RFC 3339 format"},
			}},
			want: []string{"doc:Thing.Value"},
		},
		{
			name: "unconfigured time format phrase is retained",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": map[string]any{
						"type":        "string",
						"format":      "date",
						"description": "Timestamp in a date format.",
					},
				}),
			}, nil),
			goSrc: goFile(
				`import "time"`,
				goStruct("Thing", "", goField("Value", "time.Time", "value", "Timestamp.")),
			),
			cfg: Config{FormatPhrases: map[string][]string{
				"date-time": {" in RFC 3339 format"},
			}},
			want: []string{"doc:Thing.Value"},
		},
		{
			name: "nullable branch description",
			spec: apiSpec(map[string]any{
				"Ref": map[string]any{
					"type":        "string",
					"description": "Referenced schema.",
				},
				"Thing": objectSchema("", map[string]any{
					"value": map[string]any{
						"oneOf": []any{
							map[string]any{"type": "null"},
							map[string]any{
								"$ref":        "#/components/schemas/Ref",
								"description": "Nullable value.",
							},
						},
					},
				}),
			}, nil),
			goSrc: goFile(
				"// Referenced schema.\ntype Ref string",
				goStruct("Thing", "", goField("Value", "*Ref", "value", "Nullable value.")),
			),
		},
		{
			name: "bare reference does not inherit target description",
			spec: apiSpec(map[string]any{
				"Ref": map[string]any{
					"type":        "string",
					"description": "Referenced schema.",
				},
				"Thing": objectSchema("", map[string]any{
					"value": map[string]any{"$ref": "#/components/schemas/Ref"},
				}),
			}, nil),
			goSrc: goFile(
				"// Referenced schema.\ntype Ref string",
				goStruct("Thing", "", goField("Value", "*Ref", "value", "Field-specific wording.")),
			),
		},
		{
			name: "oneOf object properties merge",
			spec: apiSpec(map[string]any{
				"Thing": map[string]any{
					"oneOf": []any{
						map[string]any{
							"type": "object",
							"properties": map[string]any{
								"present": stringProperty("Present field."),
							},
						},
						map[string]any{
							"type": "object",
							"properties": map[string]any{
								"missing": stringProperty("Missing field."),
							},
						},
					},
				},
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Present", "string", "present", "Present field."))),
			want:  []string{"missing-field:Thing.missing"},
		},
		{
			name: "omitempty JSON tag matches",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": stringProperty("Value description."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value,omitempty", "Value description."))),
		},
		{
			name: "field is matched by JSON tag not Go identifier",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": stringProperty("Value description."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("DifferentName", "string", "value", "Value description."))),
		},
		{
			name: "ignored and untagged fields are excluded",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", nil),
			}, nil),
			goSrc: goFile(goStruct("Thing", "",
				goField("Ignored", "string", "-", ""),
				goField("Untagged", "int", "", ""),
			)),
		},
		{
			name: "extra Go field",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", nil),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Extra", "string", "extra", ""))),
			want:  []string{"extra-field:Thing.Extra"},
		},
		{
			name: "missing Go field",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": map[string]any{"type": "string"},
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", "")),
			want:  []string{"missing-field:Thing.value"},
		},
		{
			name: "accepted extra field",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", nil),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Extra", "string", "extra", ""))),
			cfg: Config{Accepted: []Accepted{{
				Kind: "extra-field", Target: "Thing.Extra", Reason: "SDK-owned field.",
			}}},
		},
		{
			name: "accepted extra field becomes stale",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": map[string]any{"type": "string"},
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value", ""))),
			cfg: Config{Accepted: []Accepted{{
				Kind: "extra-field", Target: "Thing.Value", Reason: "SDK-owned field.",
			}}},
			want: []string{"stale-config:accepted.extra-field.Thing.Value"},
		},
		{
			name: "mapped query parameter matches",
			spec: operationSpec("list_widgets", operationParameter("limit", "query", "Maximum results.")),
			goSrc: goFile(goStruct("ListWidgetsArgs", "",
				goField("Limit", "int", "limit", "Maximum results."),
			)),
			cfg: Config{Operations: map[string]string{"list_widgets": "ListWidgetsArgs"}},
		},
		{
			name:  "query parameter missing from Go",
			spec:  operationSpec("list_widgets", operationParameter("limit", "query", "")),
			goSrc: goFile(goStruct("ListWidgetsArgs", "")),
			cfg:   Config{Operations: map[string]string{"list_widgets": "ListWidgetsArgs"}},
			want:  []string{"missing-field:ListWidgetsArgs.limit"},
		},
		{
			name: "path parameter field is not extra",
			spec: operationSpec(
				"list_widgets",
				operationParameter("widget", "path", ""),
				operationParameter("limit", "query", "Maximum results."),
			),
			goSrc: goFile(goStruct("ListWidgetsArgs", "",
				goField("Widget", "string", "widget", ""),
				goField("Limit", "int", "limit", "Maximum results."),
			)),
			cfg: Config{Operations: map[string]string{"list_widgets": "ListWidgetsArgs"}},
		},
		{
			name: "unmatched Go query field is extra",
			spec: operationSpec("list_widgets", operationParameter("limit", "query", "")),
			goSrc: goFile(goStruct("ListWidgetsArgs", "",
				goField("Limit", "int", "limit", ""),
				goField("Extra", "string", "extra", ""),
			)),
			cfg:  Config{Operations: map[string]string{"list_widgets": "ListWidgetsArgs"}},
			want: []string{"extra-field:ListWidgetsArgs.Extra"},
		},
		{
			name: "query parameter documentation drift",
			spec: operationSpec("list_widgets", operationParameter("limit", "query", "Maximum results.")),
			goSrc: goFile(goStruct("ListWidgetsArgs", "",
				goField("Limit", "int", "limit", "Number of results."),
			)),
			cfg:  Config{Operations: map[string]string{"list_widgets": "ListWidgetsArgs"}},
			want: []string{"doc:ListWidgetsArgs.Limit"},
		},
		{
			name:  "unmapped operation with query parameters",
			spec:  operationSpec("list_widgets", operationParameter("limit", "query", "")),
			goSrc: goFile(),
			want:  []string{"unmapped-operation:list_widgets"},
		},
		{
			name:  "ignored operation with query parameters",
			spec:  operationSpec("read_widgets", operationParameter("limit", "query", "")),
			goSrc: goFile(),
			cfg: Config{IgnoreOperations: map[string]string{
				"read_widgets": "Parameters are modeled by read options.",
			}},
		},
		{
			name:  "unmapped operation with only path parameters",
			spec:  operationSpec("get_widget", operationParameter("widget", "path", "")),
			goSrc: goFile(),
		},
		{
			name: "typed enum values match",
			spec: apiSpec(map[string]any{
				"Color": map[string]any{"type": "string", "enum": []string{"red", "blue"}},
			}, nil),
			goSrc: goFile(
				"type Color string",
				"const ColorRed Color = \"red\"\nconst ColorBlue Color = \"blue\"",
			),
		},
		{
			name: "enum value only in spec",
			spec: apiSpec(map[string]any{
				"Color": map[string]any{"type": "string", "enum": []string{"red", "blue"}},
			}, nil),
			goSrc: goFile(
				"type Color string",
				"const ColorRed Color = \"red\"",
			),
			want: []string{"enum:Color.blue"},
		},
		{
			name: "enum value only in Go",
			spec: apiSpec(map[string]any{
				"Color": map[string]any{"type": "string", "enum": []string{"red"}},
			}, nil),
			goSrc: goFile(
				"type Color string",
				"const ColorRed Color = \"red\"\nconst ColorGreen Color = \"green\"",
			),
			want: []string{"enum:Color.green"},
		},
		{
			name: "valued untyped const does not inherit previous type",
			spec: apiSpec(map[string]any{
				"Color": map[string]any{"type": "string", "enum": []string{"red", "green"}},
			}, nil),
			goSrc: goFile(
				"type Color string",
				"const (\nColorRed Color = \"red\"\nColorGreen = \"green\"\n)",
			),
			want: []string{"enum:Color.green"},
		},
		{
			name: "untyped enum prefix excludes typed consts",
			spec: apiSpec(map[string]any{
				"Operation": map[string]any{"type": "string", "enum": []string{"run"}},
			}, nil),
			goSrc: goFile(
				"type Operation string",
				"const OperationTyped Operation = \"other\"\nconst OperationUntyped = \"run\"",
			),
			cfg: Config{UntypedEnums: map[string]string{"Operation": "Operation"}},
		},
		{
			name: "enum schema without a Go type is unmapped",
			spec: apiSpec(map[string]any{
				"Color": map[string]any{"type": "string", "enum": []string{"red"}},
			}, nil),
			goSrc: goFile(),
			want:  []string{"unmapped-schema:Color"},
		},
		{
			name: "schema rename maps to Go type",
			spec: apiSpec(map[string]any{
				"WidgetSchema": objectSchema("A widget.", nil),
			}, nil),
			goSrc: goFile(goStruct("Widget", "A widget.")),
			cfg:   Config{Renames: map[string]string{"WidgetSchema": "Widget"}},
		},
		{
			name: "skip type ignores docs and fields",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("Spec description.", map[string]any{
					"expected": stringProperty("Spec field."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "Go description.",
				goField("Extra", "string", "extra", "Go field."),
			)),
			cfg: Config{SkipTypes: map[string]string{"Thing": "SDK-owned shape."}},
		},
		{
			name: "ignored schema is not reported",
			spec: apiSpec(map[string]any{
				"Unused": map[string]any{"type": "string"},
			}, nil),
			goSrc: goFile(),
			cfg:   Config{IgnoreSchemas: map[string]string{"Unused": "Intentionally unmodeled."}},
		},
		{
			name:  "rename with missing schema is stale",
			spec:  apiSpec(nil, nil),
			goSrc: goFile(goStruct("Thing", "")),
			cfg:   Config{Renames: map[string]string{"MissingSchema": "Thing"}},
			want:  []string{"stale-config:renames.MissingSchema"},
		},
		{
			name: "rename with missing Go type is stale",
			spec: apiSpec(map[string]any{
				"Thing": map[string]any{"type": "string"},
			}, nil),
			goSrc: goFile(),
			cfg: Config{
				Renames:       map[string]string{"Thing": "MissingGoType"},
				IgnoreSchemas: map[string]string{"Thing": "Keep this case focused on stale mappings."},
			},
			want: []string{"stale-config:renames.Thing"},
		},
		{
			name:  "operation mapping with missing operation is stale",
			spec:  apiSpec(nil, nil),
			goSrc: goFile(goStruct("Args", "")),
			cfg:   Config{Operations: map[string]string{"old_operation": "Args"}},
			want:  []string{"stale-config:operations.old_operation"},
		},
		{
			name:  "operation mapping with missing Go type is stale",
			spec:  operationSpec("list_widgets", operationParameter("limit", "query", "")),
			goSrc: goFile(),
			cfg:   Config{Operations: map[string]string{"list_widgets": "MissingArgs"}},
			want:  []string{"stale-config:operations.list_widgets"},
		},
		{
			name:  "unknown ignored operation is stale",
			spec:  apiSpec(nil, nil),
			goSrc: goFile(),
			cfg:   Config{IgnoreOperations: map[string]string{"missing_operation": "No longer used."}},
			want:  []string{"stale-config:ignoreOperations.missing_operation"},
		},
		{
			name:  "unknown skipped type is stale",
			spec:  apiSpec(nil, nil),
			goSrc: goFile(),
			cfg:   Config{SkipTypes: map[string]string{"MissingType": "No longer used."}},
			want:  []string{"stale-config:skipTypes.MissingType"},
		},
		{
			name:  "unknown ignored schema is stale",
			spec:  apiSpec(nil, nil),
			goSrc: goFile(),
			cfg:   Config{IgnoreSchemas: map[string]string{"MissingSchema": "No longer used."}},
			want:  []string{"stale-config:ignoreSchemas.MissingSchema"},
		},
		{
			name:  "untyped enum for missing schema is stale",
			spec:  apiSpec(nil, nil),
			goSrc: goFile(`const OperationRun = "run"`),
			cfg:   Config{UntypedEnums: map[string]string{"MissingSchema": "Operation"}},
			want:  []string{"stale-config:untypedEnums.MissingSchema"},
		},
		{
			name: "untyped enum with unmatched prefix is stale",
			spec: apiSpec(map[string]any{
				"Operation": map[string]any{"type": "string"},
			}, nil),
			goSrc: goFile(),
			cfg:   Config{UntypedEnums: map[string]string{"Operation": "NoConstants"}},
			want:  []string{"stale-config:untypedEnums.Operation"},
		},
		{
			name:  "accepted entry with unknown kind is stale",
			spec:  apiSpec(nil, nil),
			goSrc: goFile(),
			cfg: Config{Accepted: []Accepted{{
				Kind: "unknown", Target: "Thing.value", Reason: "Invalid kind.",
			}}},
			want: []string{"stale-config:accepted.unknown.Thing.value"},
		},
		{
			name:  "accepted doc target is stale when missing",
			spec:  apiSpec(nil, nil),
			goSrc: goFile(),
			cfg: Config{Accepted: []Accepted{{
				Kind: "doc", Target: "Missing.value", Spec: "spec", Go: "go", Reason: "No target.",
			}}},
			want: []string{"stale-config:accepted.doc.Missing.value"},
		},
		{
			name: "accepted docs use collapsed whitespace",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": stringProperty("Spec   docs."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value", "Go\n docs."))),
			cfg: Config{Accepted: []Accepted{{
				Kind: "doc", Target: "Thing.Value",
				Spec: "Spec docs.", Go: "Go docs.", Reason: "Intentional wording.",
			}}},
		},
		{
			name: "accepted Go text change is stale",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": stringProperty("Spec docs."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goField("Value", "string", "value", "Current Go docs."))),
			cfg: Config{Accepted: []Accepted{{
				Kind: "doc", Target: "Thing.Value",
				Spec: "Spec docs.", Go: "Old Go docs.", Reason: "Previous wording.",
			}}},
			want: []string{
				"doc:Thing.Value",
				"stale-config:accepted.doc.Thing.Value",
			},
		},
		{
			name: "grouped type declaration uses declaration documentation",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("Grouped type docs.", nil),
			}, nil),
			goSrc: goFile("// Grouped type docs.\ntype (\nThing struct{}\n)"),
		},
		{
			name: "trailing field comment is used",
			spec: apiSpec(map[string]any{
				"Thing": objectSchema("", map[string]any{
					"value": stringProperty("Trailing field docs."),
				}),
			}, nil),
			goSrc: goFile(goStruct("Thing", "", goTrailingField("Value", "string", "value", "Trailing field docs."))),
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := runCheck(t, testCase.spec, testCase.goSrc, testCase.cfg)
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("findings = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestLoadGoSourceIgnoresTestFiles(t *testing.T) {
	spec := apiSpec(map[string]any{
		"Thing": objectSchema("", map[string]any{
			"value": stringProperty("Value description."),
		}),
	}, nil)
	goFiles := map[string]string{
		"types.go": goFile(goStruct("Thing", "",
			goField("Value", "string", "value", "Value description."),
		)),
		"types_test.go": goFile(goStruct("Thing", "",
			goField("TestOnly", "string", "test_only", ""),
		)),
	}
	if got := findingKeys(runCheckWithFiles(t, spec, goFiles, Config{})); len(got) != 0 {
		t.Fatalf("findings = %v, want none", got)
	}
}

func TestParseConstantsFollowsGoSpec(t *testing.T) {
	goSrc := goFile(
		"type Color string",
		"const (\nA Color = \"x\"\nB\n)",
		"const (\nC Color = \"a\"\nD = \"b\"\n)",
	)
	_, _, goSource := loadCheckFixture(t, apiSpec(nil, nil), map[string]string{"types.go": goSrc}, Config{})
	got := make(map[string]string, len(goSource.Constants))
	for _, constant := range goSource.Constants {
		got[constant.Name] = constant.Type + ":" + constant.Value
	}
	want := map[string]string{
		"A": "Color:x",
		"B": "Color:x",
		"C": "Color:a",
		"D": ":b",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed constants = %v, want %v", got, want)
	}
}

func TestCheckDeterministic(t *testing.T) {
	paths := operationSpec("unmapped_operation", operationParameter("limit", "query", ""))["paths"].(map[string]any)
	paths["/list-widgets"] = map[string]any{
		"get": map[string]any{
			"operationId": "list_widgets",
			"parameters": []any{
				operationParameter("limit", "query", "Maximum results."),
			},
		},
	}
	spec := apiSpec(map[string]any{
		"A": objectSchema("Spec A docs.", map[string]any{
			"value":   stringProperty("Spec A field."),
			"missing": map[string]any{"type": "string"},
		}),
		"B": objectSchema("", map[string]any{
			"value": stringProperty("Spec B field."),
		}),
		"Unmapped": map[string]any{"type": "string"},
	}, paths)
	goFiles := map[string]string{
		"a.go": goFile(goStruct("A", "Go A docs.",
			goField("Value", "string", "value", "Go A field."),
			goField("Extra", "string", "extra", ""),
		)),
		"b.go": goFile(goStruct("B", "",
			goField("Value", "string", "value", "Go B field."),
		)),
		"c.go": goFile(goStruct("ListWidgetsArgs", "",
			goField("Limit", "int", "limit", "Maximum results."),
			goField("ExtraA", "string", "extra_a", ""),
			goField("ExtraB", "string", "extra_b", ""),
			goField("ExtraC", "string", "extra_c", ""),
		)),
	}
	cfg := Config{
		Operations: map[string]string{
			"list_widgets": "ListWidgetsArgs",
		},
	}
	cfg, loadedSpec, goSource := loadCheckFixture(t, spec, goFiles, cfg)

	var want []Finding
	for iteration := range 20 {
		findings := check(cfg, loadedSpec, goSource)
		if iteration == 0 {
			want = findings
			if len(findings) < 8 {
				t.Fatalf("fixture produced only %d findings, want several", len(findings))
			}
			kinds := make(map[string]bool, 8)
			files := make(map[string]bool, 3)
			for _, finding := range findings {
				kinds[finding.Kind] = true
				for _, file := range []string{"a.go", "b.go", "c.go"} {
					if strings.Contains(finding.Pos, string(filepath.Separator)+file+":") {
						files[file] = true
					}
				}
			}
			if len(kinds) < 4 || len(files) != 3 {
				t.Fatalf("fixture covered %d kinds and files %v, want at least four kinds across all three files", len(kinds), files)
			}
			continue
		}
		if !reflect.DeepEqual(findings, want) {
			t.Fatalf("findings changed on iteration %d:\n%v\nwant:\n%v", iteration, findings, want)
		}
	}
}
