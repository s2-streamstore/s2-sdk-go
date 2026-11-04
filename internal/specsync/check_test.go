package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
					"Color":    map[string]any{"type": "string", "enum": []string{"red", "blue"}},
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

	initial := findingKeys(loadFindings("accepted spec"))
	wantInitial := []string{
		"doc:Widget.Known",
		"enum:Color.blue",
		"extra-field:Widget.Extra",
		"missing-field:Widget.missing",
		"unmapped-schema:Unmapped",
	}
	if !reflect.DeepEqual(initial, wantInitial) {
		t.Fatalf("initial findings = %v, want %v", initial, wantInitial)
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
	keys := make([]string, 0, len(findings))
	for _, finding := range findings {
		keys = append(keys, finding.Kind+":"+finding.Target)
	}
	sort.Strings(keys)
	return keys
}
