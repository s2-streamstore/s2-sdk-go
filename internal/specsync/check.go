package main

import (
	"fmt"
	"go/token"
	"regexp"
	"sort"
	"strings"
)

const configPos = "internal/specsync/config.json"

type Finding struct {
	Kind   string
	Target string
	Spec   string
	Go     string
	Pos    string
}

type docPair struct {
	spec   string
	goText string
	pos    string
	format string
	isTime bool
}

type checker struct {
	cfg           Config
	spec          Spec
	goSource      GoSource
	findings      []Finding
	docPairs      map[string]docPair
	extraFields   map[string]bool
	acceptedByKey map[string]Accepted
}

var markdownLink = regexp.MustCompile(`\[([^\]]+)\]\([^\s)]+\)`)

func check(cfg Config, spec Spec, goSource GoSource) []Finding {
	c := checker{
		cfg:           cfg,
		spec:          spec,
		goSource:      goSource,
		docPairs:      make(map[string]docPair),
		extraFields:   make(map[string]bool),
		acceptedByKey: make(map[string]Accepted),
	}
	for _, accepted := range cfg.Accepted {
		key := accepted.Kind + "\x00" + accepted.Target
		if _, exists := c.acceptedByKey[key]; !exists {
			c.acceptedByKey[key] = accepted
		}
	}
	c.validateConfig()
	c.checkSchemas()
	c.checkOperations()
	c.checkAccepted()
	sort.Slice(c.findings, func(i, j int) bool {
		left, right := c.findings[i], c.findings[j]
		if left.Pos != right.Pos {
			return left.Pos < right.Pos
		}
		if left.Target != right.Target {
			return left.Target < right.Target
		}
		return left.Kind < right.Kind
	})
	return c.findings
}

func (c *checker) validateConfig() {
	for schemaName, goName := range c.cfg.Renames {
		if _, exists := c.spec.Schemas[schemaName]; !exists {
			c.configFinding("stale-config", "renames."+schemaName)
		}
		if _, exists := c.goSource.Types[goName]; !exists {
			c.configFinding("stale-config", "renames."+schemaName)
		}
	}
	for operationID, goName := range c.cfg.Operations {
		if !c.hasOperation(operationID) {
			c.configFinding("stale-config", "operations."+operationID)
		}
		if _, exists := c.goSource.Types[goName]; !exists {
			c.configFinding("stale-config", "operations."+operationID)
		}
	}
	for operationID := range c.cfg.IgnoreOperations {
		if !c.hasOperation(operationID) {
			c.configFinding("stale-config", "ignoreOperations."+operationID)
		}
	}
	for typeName := range c.cfg.SkipTypes {
		if _, exists := c.goSource.Types[typeName]; !exists {
			c.configFinding("stale-config", "skipTypes."+typeName)
		}
	}
	for schemaName := range c.cfg.IgnoreSchemas {
		if _, exists := c.spec.Schemas[schemaName]; !exists {
			c.configFinding("stale-config", "ignoreSchemas."+schemaName)
		}
	}
	for schemaName, prefix := range c.cfg.UntypedEnums {
		if _, exists := c.spec.Schemas[schemaName]; !exists {
			c.configFinding("stale-config", "untypedEnums."+schemaName)
		}
		if !c.hasConstantPrefix(prefix) {
			c.configFinding("stale-config", "untypedEnums."+schemaName)
		}
	}
}

func (c *checker) checkSchemas() {
	schemaNames := sortedKeys(c.spec.Schemas)
	for _, schemaName := range schemaNames {
		schema := c.spec.Schemas[schemaName]
		if _, ignored := c.cfg.IgnoreSchemas[schemaName]; ignored {
			continue
		}
		goName := c.goName(schemaName)
		goType, hasGoType := c.goSource.Types[goName]
		if !hasGoType {
			if _, untypedEnum := c.cfg.UntypedEnums[schemaName]; untypedEnum {
				c.checkEnum(schemaName, schema, "")
				continue
			}
			c.add(Finding{Kind: "unmapped-schema", Target: schemaName, Pos: configPos + ":1"})
			continue
		}
		if _, skipped := c.cfg.SkipTypes[goName]; skipped {
			continue
		}
		if schema.Description != "" {
			c.compareDoc(goName, schema.Description, goType.Doc, "", false, goType.Pos)
		}
		c.checkFields(goName, schema.properties(), goType.Fields, goType.Pos.String())
		c.checkEnum(schemaName, schema, goType.Pos.String())
	}
}

func (c *checker) checkFields(typeName string, properties map[string]SchemaNode, fields map[string]GoField, typePosition string) {
	for _, propertyName := range sortedKeys(properties) {
		property := properties[propertyName]
		field, exists := fields[propertyName]
		target := typeName + "." + propertyName
		if !exists {
			c.add(Finding{Kind: "missing-field", Target: target, Pos: typePosition})
			continue
		}
		if description := property.propertyDescription(); description != "" {
			c.compareDoc(typeName+"."+field.Name, description, field.Doc, property.format(), field.Time, field.Pos)
		}
	}
	for _, fieldName := range sortedKeys(fields) {
		field := fields[fieldName]
		if _, exists := properties[fieldName]; exists {
			continue
		}
		target := typeName + "." + field.Name
		c.extraFields[target] = true
		if _, accepted := c.acceptedByKey["extra-field\x00"+target]; accepted {
			continue
		}
		c.add(Finding{Kind: "extra-field", Target: target, Pos: field.Pos.String()})
	}
}

func (c *checker) checkOperations() {
	for _, operation := range c.spec.Operations {
		queryParams := make([]Parameter, 0, len(operation.Parameters))
		pathParams := make(map[string]bool)
		for _, parameter := range operation.Parameters {
			switch parameter.In {
			case "query":
				queryParams = append(queryParams, parameter)
			case "path":
				pathParams[parameter.Name] = true
			}
		}
		goName, mapped := c.cfg.Operations[operation.ID]
		if len(queryParams) == 0 && !mapped {
			continue
		}
		if !mapped {
			if _, ignored := c.cfg.IgnoreOperations[operation.ID]; !ignored {
				c.add(Finding{Kind: "unmapped-operation", Target: operation.ID, Pos: configPos + ":1"})
			}
			continue
		}
		goType, exists := c.goSource.Types[goName]
		if !exists {
			continue
		}
		queryNames := make(map[string]bool)
		for _, parameter := range queryParams {
			queryNames[parameter.Name] = true
			field, exists := goType.Fields[parameter.Name]
			target := goName + "." + parameter.Name
			if !exists {
				c.add(Finding{Kind: "missing-field", Target: target, Pos: goType.Pos.String()})
				continue
			}
			if parameter.Description != "" {
				c.compareDoc(goName+"."+field.Name, parameter.Description, field.Doc, parameter.Format, field.Time, field.Pos)
			}
		}
		for tag, field := range goType.Fields {
			if queryNames[tag] || pathParams[tag] {
				continue
			}
			target := goName + "." + field.Name
			c.extraFields[target] = true
			if _, accepted := c.acceptedByKey["extra-field\x00"+target]; accepted {
				continue
			}
			c.add(Finding{Kind: "extra-field", Target: target, Pos: field.Pos.String()})
		}
	}
}

func (c *checker) compareDoc(target, specText, goText, format string, isTime bool, position token.Position) {
	pos := position.String()
	pair := docPair{spec: specText, goText: goText, pos: pos, format: format, isTime: isTime}
	c.docPairs[target] = pair
	if specText == "" || normalize(specText, format, isTime, c.cfg.FormatPhrases) == normalize(goText, format, isTime, c.cfg.FormatPhrases) {
		return
	}
	accepted, exists := c.acceptedByKey["doc\x00"+target]
	if exists && sameText(specText, accepted.Spec) && sameText(goText, accepted.Go) {
		return
	}
	c.add(Finding{
		Kind:   "doc",
		Target: target,
		Spec:   collapse(specText),
		Go:     collapse(goText),
		Pos:    pos,
	})
}

func (c *checker) checkEnum(schemaName string, schema SchemaNode, goPosition string) {
	if len(schema.Enum) == 0 {
		return
	}
	specValues := make(map[string]bool, len(schema.Enum))
	for _, value := range schema.Enum {
		specValues[value] = true
	}
	goValues := make(map[string]bool)
	if prefix, untyped := c.cfg.UntypedEnums[schemaName]; untyped {
		for _, constant := range c.goSource.Constants {
			if constant.Type == "" && strings.HasPrefix(constant.Name, prefix) {
				goValues[constant.Value] = true
				if goPosition == "" {
					goPosition = constant.Pos.String()
				}
			}
		}
	} else {
		goName := c.goName(schemaName)
		for _, constant := range c.goSource.Constants {
			if constant.Type == goName {
				goValues[constant.Value] = true
			}
		}
	}
	if goPosition == "" {
		goPosition = configPos + ":1"
	}
	for _, value := range sortedKeys(specValues) {
		if !goValues[value] {
			c.add(Finding{Kind: "enum", Target: schemaName + "." + value, Spec: value, Pos: goPosition})
		}
	}
	for _, value := range sortedKeys(goValues) {
		if !specValues[value] {
			c.add(Finding{Kind: "enum", Target: schemaName + "." + value, Go: value, Pos: goPosition})
		}
	}
}

func (c *checker) checkAccepted() {
	for _, accepted := range c.cfg.Accepted {
		var valid bool
		switch accepted.Kind {
		case "doc":
			pair, exists := c.docPairs[accepted.Target]
			valid = exists && sameText(pair.spec, accepted.Spec) && sameText(pair.goText, accepted.Go)
			if !valid {
				finding := Finding{Kind: "stale-config", Target: "accepted." + accepted.Kind + "." + accepted.Target, Pos: configPos + ":1"}
				if exists {
					finding.Spec = collapse(pair.spec)
					finding.Go = collapse(pair.goText)
					finding.Pos = pair.pos
				}
				c.add(finding)
			}
		case "extra-field":
			valid = c.extraFields[accepted.Target]
			if !valid {
				c.configFinding("stale-config", "accepted."+accepted.Kind+"."+accepted.Target)
			}
		default:
			c.configFinding("stale-config", "accepted."+accepted.Kind+"."+accepted.Target)
		}
	}
}

func (c *checker) goName(schemaName string) string {
	if name, exists := c.cfg.Renames[schemaName]; exists {
		return name
	}
	return schemaName
}

func (c *checker) hasOperation(id string) bool {
	for _, operation := range c.spec.Operations {
		if operation.ID == id {
			return true
		}
	}
	return false
}

func (c *checker) hasConstantPrefix(prefix string) bool {
	for _, constant := range c.goSource.Constants {
		if constant.Type == "" && strings.HasPrefix(constant.Name, prefix) {
			return true
		}
	}
	return false
}

func (c *checker) configFinding(kind, target string) {
	c.add(Finding{Kind: kind, Target: target, Pos: configPos + ":1"})
}

func (c *checker) add(finding Finding) {
	c.findings = append(c.findings, finding)
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func collapse(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

func normalize(text, format string, isTime bool, formatPhrases map[string][]string) string {
	text = markdownLink.ReplaceAllString(text, "$1")
	text = strings.ReplaceAll(text, "`", "")
	if isTime {
		for _, phrase := range formatPhrases[format] {
			text = strings.ReplaceAll(text, phrase, "")
		}
	}
	return collapse(text)
}

func sameText(left, right string) bool {
	return collapse(left) == collapse(right)
}

func (n SchemaNode) format() string {
	if n.Format != "" {
		return n.Format
	}
	for _, branches := range [][]SchemaNode{n.OneOf, n.AnyOf} {
		for _, branch := range branches {
			if branch.Format != "" {
				return branch.Format
			}
		}
	}
	return ""
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s %s", f.Pos, f.Kind, f.Target)
}
