package main

import (
	"encoding/json"
	"fmt"
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

var (
	entryFuncs = []string{
		"http.HandleFunc",
		"ListenAndServe",
		"Run",
		"Serve",
	}

	readFuncs = []string{
		"os.Open",
		"os.ReadFile",
		"Read",
		"Query",
		"Scan",
	}

	writeFuncs = []string{
		"os.Create",
		"os.WriteFile",
		"Write",
		"Print",
		"Printf",
		"Encode",
		"Respond",
	}
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println(`{"entries":0,"exits":0,"reads":0,"writes":0}`)
		return
	}

	root := os.Args[1]
	fset := token.NewFileSet()
	result := Result{}

	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || !strings.HasSuffix(path, ".go") {
			return nil
		}

		node, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return nil
		}

		ast.Inspect(node, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}

			name := getCallName(call.Fun)

			for _, f := range entryFuncs {
				if strings.Contains(name, f) {
					result.Entries++
				}
			}

			for _, f := range readFuncs {
				if strings.Contains(name, f) {
					result.Reads++
				}
			}

			for _, f := range writeFuncs {
				if strings.Contains(name, f) {
					result.Writes++
				}
			}

			// EXIT heuristic: returns + os.Exit()
			if strings.Contains(name, "os.Exit") {
				result.Exits++
			}

			return true
		})

		return nil
	})

	out, _ := json.Marshal(result)
	fmt.Println(string(out))
}

func getCallName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.SelectorExpr:
		return getCallName(e.X) + "." + e.Sel.Name
	case *ast.Ident:
		return e.Name
	default:
		return ""
	}
}
