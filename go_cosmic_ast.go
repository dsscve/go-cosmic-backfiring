package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

type Result struct {
	Entries int `json:"entries"`
	Exits   int `json:"exits"`
	Reads   int `json:"reads"`
	Writes  int `json:"writes"`
}

func callName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.SelectorExpr:
		if ident, ok := fun.X.(*ast.Ident); ok {
			return ident.Name + "." + fun.Sel.Name
		}
	case *ast.Ident:
		return fun.Name
	}
	return ""
}

func isEntryNode(n ast.Node) bool {
	switch x := n.(type) {

	// HTTP handlers: http.HandleFunc(...)
	case *ast.CallExpr:
		name := callName(x)
		if strings.Contains(name, "HandleFunc") ||
			strings.Contains(name, "ListenAndServe") ||
			strings.Contains(name, "Run") { // Gin/Fiber/Echo
			return true
		}

	// main()
	case *ast.FuncDecl:
		if x.Name.Name == "main" {
			return true
		}
	}
	return false
}

func isReadCall(name string) bool {
	readCalls := []string{
		"Read", "ReadAll", "Open", "Get",
		"Query", "QueryRow",
		"Scan",
		"Decode",
		"Find", "First", // ORM
	}
	for _, c := range readCalls {
		if strings.Contains(name, c) {
			return true
		}
	}
	return false
}

func isWriteCall(name string) bool {
	writeCalls := []string{
		"Write", "Create", "Post",
		"Exec",
		"Encode",
		"Save", "Update", // ORM
		"Printf", "Fprintf",
	}
	for _, c := range writeCalls {
		if strings.Contains(name, c) {
			return true
		}
	}
	return false
}

func isExitCall(name string) bool {
	exitCalls := []string{
		"Write", "Print", "Fatal",
		"Exit",
		"Respond", "Send",
	}
	for _, c := range exitCalls {
		if strings.Contains(name, c) {
			return true
		}
	}
	return false
}

func main() {
	if len(os.Args) < 2 {
		os.Exit(1)
	}
	root := os.Args[1]
	fset := token.NewFileSet()
	result := Result{}

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || !strings.HasSuffix(path, ".go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}

		ast.Inspect(file, func(n ast.Node) bool {

			if isEntryNode(n) {
				result.Entries++
			}

			if call, ok := n.(*ast.CallExpr); ok {
				name := callName(call)

				if isReadCall(name) {
					result.Reads++
				}
				if isWriteCall(name) {
					result.Writes++
				}
				if isExitCall(name) {
					result.Exits++
				}
			}

			return true
		})

		return nil
	})

	json.NewEncoder(os.Stdout).Encode(result)
}
