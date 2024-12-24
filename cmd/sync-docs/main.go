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
	for i, line := range lines {
		if i == 0 {
			// Because of pre-existing "//"
			continue
		}
		tab := ""
		for i := uint(0); i < indent; i++ {
			tab += "\t"
		}
		lines[i] = tab + "// " + strings.TrimSpace(line)
	}
	return strings.Join(lines, "\n")
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
		switch t := n.(type) {
		case *ast.GenDecl:
			// Handle type declarations
			for _, spec := range t.Specs {
				switch typeSpec := spec.(type) {
				case *ast.ValueSpec:
					if typeSpec.Doc != nil {
						for _, name := range typeSpec.Names {
							commentMap[name.Name] = strings.TrimSpace(typeSpec.Doc.Text())
						}
					}
				case *ast.TypeSpec:
					// Add type comment
					if t.Doc != nil {
						commentMap[typeSpec.Name.Name] = strings.TrimSpace(t.Doc.Text())
					}

					switch ts := typeSpec.Type.(type) {
					case *ast.StructType:
						// Handle struct fields
						for _, field := range ts.Fields.List {
							if field.Doc != nil && len(field.Names) > 0 {
								for _, name := range field.Names {
									key := fmt.Sprintf("%s.%s", typeSpec.Name.Name, name.Name)
									commentMap[key] = strings.TrimSpace(field.Doc.Text())
								}
							}
						}
					case *ast.InterfaceType:
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
				}
			}
		case *ast.FuncDecl:
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
		return true
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

// syncDoc looks up and formats the comment for a given key
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
	err = os.WriteFile(*outputFile, []byte(output), 0644)
	if err != nil {
		fmt.Printf("Error writing to output file: %v\n", err)
		os.Exit(1)
	}
}
