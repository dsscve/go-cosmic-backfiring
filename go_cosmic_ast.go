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

func isReadCall(name string) bool {
	readCalls := []string{
		"os.Open", "ioutil.ReadFile", "os.ReadFile",
		"db.Query", "db.QueryRow",
		"http.Get", "client.Do",
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
		"os.Create", "ioutil.WriteFile", "os.WriteFile",
		"db.Exec",
		"http.Post", "client.Post",
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
		"fmt.Println", "log.Println", "log.Fatal",
		"os.Exit", "w.Write", "json.NewEncoder",
	}
	for _, c := range exitCalls {
		if strings.Contains(name, c) {
			return true
		}
	}
	return false
}

func isEntryFunc(fn *ast.FuncDecl) bool {
	if fn.Name.Name == "main" {
		return true
	}
	if fn.Recv == nil {
		return false
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

			// Entry points
			if fn, ok := n.(*ast.FuncDecl); ok {
				if isEntryFunc(fn) {
					result.Entries++
				}
			}

			// Function calls
			if call, ok := n.(*ast.CallExpr); ok {
				var name string

				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					if ident, ok := fun.X.(*ast.Ident); ok {
						name = ident.Name + "." + fun.Sel.Name
					}
				case *ast.Ident:
					name = fun.Name
				}

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
