package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
)

type GoSource struct {
	Types     map[string]GoType
	Constants []GoConstant
}

type GoType struct {
	Name   string
	Doc    string
	Pos    token.Position
	Fields map[string]GoField
}

type GoField struct {
	Name string
	Doc  string
	Pos  token.Position
	Time bool
}

type GoConstant struct {
	Name  string
	Value string
	Type  string
	Pos   token.Position
}

func loadGoSource(dir string) (GoSource, error) {
	source := GoSource{Types: make(map[string]GoType)}
	fileSet := token.NewFileSet()
	var paths []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) == ".go" && !strings.HasSuffix(path, "_test.go") {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return GoSource{}, fmt.Errorf("walk Go package %q: %w", dir, err)
	}
	for _, path := range paths {
		file, err := parser.ParseFile(fileSet, path, nil, parser.ParseComments)
		if err != nil {
			return GoSource{}, fmt.Errorf("parse Go source %q: %w", path, err)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok {
				continue
			}
			switch general.Tok {
			case token.TYPE:
				for _, spec := range general.Specs {
					typeSpec, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					doc := typeSpec.Doc
					if doc == nil {
						doc = general.Doc
					}
					entry := GoType{
						Name:   typeSpec.Name.Name,
						Doc:    commentText(doc),
						Pos:    fileSet.Position(typeSpec.Pos()),
						Fields: make(map[string]GoField),
					}
					if structure, ok := typeSpec.Type.(*ast.StructType); ok {
						for _, field := range structure.Fields.List {
							addGoFields(fileSet, entry.Fields, field)
						}
					}
					source.Types[entry.Name] = entry
				}
			case token.CONST:
				source.Constants = append(source.Constants, parseConstants(fileSet, general)...)
			}
		}
	}
	return source, nil
}

func addGoFields(fileSet *token.FileSet, fields map[string]GoField, field *ast.Field) {
	if field.Tag == nil || len(field.Names) == 0 {
		return
	}
	tagValue, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return
	}
	jsonTag := reflect.StructTag(tagValue).Get("json")
	name := strings.Split(jsonTag, ",")[0]
	if name == "" || name == "-" {
		return
	}
	for _, fieldName := range field.Names {
		doc := field.Doc
		if doc == nil {
			doc = field.Comment
		}
		fields[name] = GoField{
			Name: fieldName.Name,
			Doc:  commentText(doc),
			Pos:  fileSet.Position(field.Pos()),
			Time: isTimeType(field.Type),
		}
	}
}

func isTimeType(expression ast.Expr) bool {
	if pointer, ok := expression.(*ast.StarExpr); ok {
		expression = pointer.X
	}
	selector, ok := expression.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Time" {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	return ok && pkg.Name == "time"
}

func commentText(group *ast.CommentGroup) string {
	if group == nil {
		return ""
	}
	return group.Text()
}

func parseConstants(fileSet *token.FileSet, declaration *ast.GenDecl) []GoConstant {
	result := make([]GoConstant, 0, len(declaration.Specs))
	inheritedType := ""
	var inheritedValues []ast.Expr
	for _, spec := range declaration.Specs {
		valueSpec, ok := spec.(*ast.ValueSpec)
		if !ok {
			continue
		}
		currentType := inheritedType
		if valueSpec.Type != nil {
			if identifier, ok := valueSpec.Type.(*ast.Ident); ok {
				currentType = identifier.Name
			} else {
				currentType = ""
			}
		} else if len(valueSpec.Values) > 0 {
			currentType = ""
		}
		values := valueSpec.Values
		if len(values) == 0 {
			values = inheritedValues
		} else {
			inheritedValues = values
		}
		inheritedType = currentType
		for index, name := range valueSpec.Names {
			if index >= len(values) {
				continue
			}
			literal, ok := values[index].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				continue
			}
			value, err := strconv.Unquote(literal.Value)
			if err != nil {
				continue
			}
			result = append(result, GoConstant{
				Name:  name.Name,
				Value: value,
				Type:  currentType,
				Pos:   fileSet.Position(name.Pos()),
			})
		}
	}
	return result
}
