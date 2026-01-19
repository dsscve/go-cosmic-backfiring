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

// ---------------- ENTRY ----------------

func isEntry(n ast.Node) bool {
	switch x := n.(type) {

	// main()
	case *ast.FuncDecl:
		if x.Name.Name == "main" {
			return true
		}

	// http.HandleFunc, router.Handle, mux.HandleFunc
	case *ast.CallExpr:
		name := callName(x)
		if strings.Contains(name, "Handle") ||
			strings.Contains(name, "ListenAndServe") ||
			strings.Contains(name, "Run") ||
			strings.Contains(name, "Serve") {
			return true
		}
	}
	return false
}

// ---------------- READ ----------------

func isRead(name string) bool {
	readHints := []string{
		"Open", "Read", "ReadAll",
		"Get", "Do",
		"Query", "QueryRow",
		"Scan",
		"Decode",
		"Find", "First",
		"Args", "Env",
	}
	for _, h := range readHints {
		if strings.Contains(name, h) {
			return true
		}
	}
	return false
}

// ---------------- WRITE ----------------

func isWrite(name string) bool {
	writeHints := []string{
		"Write", "Create",
		"Post", "Put",
		"Exec",
		"Encode",
		"Save", "Update",
		"Printf", "Fprintf",
	}
	for _, h := range writeHints {
		if strings.Contains(name, h) {
			return true
		}
	}
	return false
}

// ---------------- EXIT ----------------

func isExit(name string) bool {
	exitHints := []string{
		"Write", "Print", "Fatal",
		"Exit",
		"Respond", "Send",
		"ServeHTTP",
	}
	for _, h := range exitHints {
		if strings.Contains(name, h) {
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

		// Ignore test files
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}

		ast.Inspect(file, func(n ast.Node) bool {

			if isEntry(n) {
				result.Entries++
			}

			if call, ok := n.(*ast.CallExpr); ok {
				name := callName(call)

				if isRead(name) {
					result.Reads++
				}
				if isWrite(name) {
					result.Writes++
				}
				if isExit(name) {
					result.Exits++
				}
			}

			return true
		})

		return nil
	})

	json.NewEncoder(os.Stdout).Encode(result)
}

