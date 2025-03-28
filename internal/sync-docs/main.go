package main

import (
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"text/template"
)

func commentString(doc string, indent uint) string {
	lines := strings.Split(doc, "\n")
	sliceAt := len(lines)

	for i, line := range lines {
		if i == 0 {
			// Because of pre-existing "//"
			continue
		}

		if strings.HasPrefix(line, "Types that are valid to be assigned to") {
			// Union type. We want this comment to be manually added.
			sliceAt = i

			for strings.TrimSpace(lines[sliceAt-1]) == "//" {
				// Remove empty lines before this comment
				sliceAt--
			}

			break
		}

		tab := ""
		for range indent {
			tab += "\t"
		}

		lines[i] = tab + "// " + strings.TrimSpace(line)
	}

	return strings.Join(lines[:sliceAt], "\n")
}

func inspectFuncDecl(t *ast.FuncDecl, commentMap map[string]string) {
	if t.Doc != nil {
		if t.Recv != nil && len(t.Recv.List) > 0 {
			// Handle methods
			recvType := getTypeName(t.Recv.List[0].Type)
			key := fmt.Sprintf("%s.%s", recvType, t.Name.Name)
			commentMap[key] = strings.TrimSpace(t.Doc.Text())
		} else {
			// Handle top-level functions
			commentMap[t.Name.Name] = strings.TrimSpace(t.Doc.Text())
		}
	}
}

func inspectValueSpec(typeSpec *ast.ValueSpec, commentMap map[string]string) {
	if typeSpec.Doc != nil {
		for _, name := range typeSpec.Names {
			commentMap[name.Name] = strings.TrimSpace(typeSpec.Doc.Text())
		}
	}
}

func inspectStructType(typeSpec *ast.TypeSpec, ts *ast.StructType, commentMap map[string]string) {
	// Handle struct fields
	for _, field := range ts.Fields.List {
		if field.Doc != nil && len(field.Names) > 0 {
			for _, name := range field.Names {
				key := fmt.Sprintf("%s.%s", typeSpec.Name.Name, name.Name)
				commentMap[key] = strings.TrimSpace(field.Doc.Text())
			}
		}
	}
}

func inspectInterfaceType(typeSpec *ast.TypeSpec, ts *ast.InterfaceType, commentMap map[string]string) {
	// Handle interface methods
	for _, method := range ts.Methods.List {
		if method.Doc != nil && len(method.Names) > 0 {
			for _, name := range method.Names {
				key := fmt.Sprintf("%s.%s", typeSpec.Name.Name, name.Name)
				commentMap[key] = strings.TrimSpace(method.Doc.Text())
			}
		}
	}
}

func inspectTypeSpec(t *ast.GenDecl, typeSpec *ast.TypeSpec, commentMap map[string]string) {
	// Add type comment
	if t.Doc != nil {
		commentMap[typeSpec.Name.Name] = strings.TrimSpace(t.Doc.Text())
	}

	switch ts := typeSpec.Type.(type) {
	case *ast.StructType:
		inspectStructType(typeSpec, ts, commentMap)
	case *ast.InterfaceType:
		inspectInterfaceType(typeSpec, ts, commentMap)
	}
}

func inspectGenDecl(t *ast.GenDecl, commentMap map[string]string) {
	// Handle type declarations
	for _, spec := range t.Specs {
		switch typeSpec := spec.(type) {
		case *ast.ValueSpec:
			inspectValueSpec(typeSpec, commentMap)
		case *ast.TypeSpec:
			inspectTypeSpec(t, typeSpec, commentMap)
		}
	}
}

func inspectAST(n ast.Node, commentMap map[string]string) bool {
	switch t := n.(type) {
	case *ast.GenDecl:
		inspectGenDecl(t, commentMap)
	case *ast.FuncDecl:
		inspectFuncDecl(t, commentMap)
	}

	return true
}

func parseComments(src string) map[string]string {
	fset := token.NewFileSet()

	node, err := parser.ParseFile(fset, "", src, parser.ParseComments)
	if err != nil {
		panic(err)
	}

	commentMap := make(map[string]string)

	// Traverse the AST to extract types and their fields/methods
	ast.Inspect(node, func(n ast.Node) bool {
		return inspectAST(n, commentMap)
	})

	return commentMap
}

func getTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return getTypeName(t.X)
	case *ast.Ident:
		return t.Name
	default:
		return ""
	}
}

// syncDoc looks up and formats the comment for a given key.
func syncDoc(commentMap map[string]string, key string, indent uint) string {
	doc, ok := commentMap[key]
	if !ok {
		return fmt.Sprintf("< Missing documentation for %q >", key)
	}

	return commentString(doc, indent)
}

func applyTemplate(tmplSrc string, commentMap map[string]string) string {
	funcMap := template.FuncMap{
		"syncDoc": func(key string) string {
			return syncDoc(commentMap, key, 0)
		},
		"syncDocIndent": func(key string) string {
			return syncDoc(commentMap, key, 1)
		},
	}

	tmpl, err := template.New("tmpl").Funcs(funcMap).Parse(tmplSrc)
	if err != nil {
		panic(err)
	}

	var buf bytes.Buffer

	err = tmpl.Execute(&buf, commentMap)
	if err != nil {
		panic(err)
	}

	return buf.String()
}

func main() {
	inputFile := flag.String("i", "", "Path to the input Go file (used for generating comments map)")
	templateFile := flag.String("t", "", "Path to the template file (used for generating code)")
	outputFile := flag.String("o", "", "Path to the output Go file (generated code)")
	flag.Parse()

	// Check if all flags are provided
	if *inputFile == "" || *templateFile == "" || *outputFile == "" {
		fmt.Println("Error: -i (input file), -t (template file), and -o (output file) flags are required.")
		flag.Usage()
		os.Exit(1)
	}

	// Read the input Go file
	src, err := os.ReadFile(*inputFile)
	if err != nil {
		fmt.Printf("Error reading input file: %v\n", err)
		os.Exit(1)
	}

	// Parse the comments and create the map
	commentMap := parseComments(string(src))

	// Read the template file
	tmplSrc, err := os.ReadFile(*templateFile)
	if err != nil {
		fmt.Printf("Error reading template file: %v\n", err)
		os.Exit(1)
	}

	// Apply the template with the comment map
	output := applyTemplate(string(tmplSrc), commentMap)

	output = "// Auto generated file. DO NOT EDIT.\n\n" + output

	// Write the generated code to the output file
	err = os.WriteFile(*outputFile, []byte(output), 0o644)
	if err != nil {
		fmt.Printf("Error writing to output file: %v\n", err)
		os.Exit(1)
	}
}
