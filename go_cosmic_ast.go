package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/token"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/pointer"
	"golang.org/x/tools/go/ssa"
	"golang.org/x/tools/go/callgraph"
)

// ProcessReport is the per-functional-process COSMIC-like counts.
type ProcessReport struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"` // package/path:func
	Entries int   `json:"entries"`
	Exits   int   `json:"exits"`
	Reads   int   `json:"reads"`
	Writes  int   `json:"writes"`
	Funcs   int   `json:"functions_included"`
}

// Output is the overall JSON structure.
type Output struct {
	TotalEntries int             `json:"total_entries"`
	TotalExits   int             `json:"total_exits"`
	TotalReads   int             `json:"total_reads"`
	TotalWrites  int             `json:"total_writes"`
	Processes    []ProcessReport `json:"processes"`
}

var (
	// Registration functions which take a handler function value (common web frameworks)
	// Map of package path -> set of function names considered as registration points.
	entryRegistrations = map[string]map[string]bool{
		"net/http": {
			"HandleFunc": true,
			"Handle":     true,
		},
		"github.com/gorilla/mux": {
			"HandleFunc": true,
			"Handle":     true,
		},
	}

	// Read-like functions by package path
	readFuncs = map[string]map[string]bool{
		"os": {
			"Open":     true,
			"ReadFile": true,
		},
		"io/ioutil": {
			"ReadFile": true,
		},
		"database/sql": {
			"Query":    true,
			"QueryRow": true,
			"Scan":     true,
		},
	}

	// Write-like functions by package path
	writeFuncs = map[string]map[string]bool{
		"os": {
			"Create":    true,
			"WriteFile": true,
		},
		"io/ioutil": {
			"WriteFile": true,
		},
		"database/sql": {
			"Exec": true,
		},
	}

	// Exit-like functions by package path
	exitFuncs = map[string]map[string]bool{
		"os": {
			"Exit": true,
		},
	}
)

func main() {
	log.SetFlags(0)
	ptrMode := flag.Bool("ptr", false, "enable pointer analysis + callgraph (resolves indirect/interface calls)")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [-ptr] <module-root-or-package-pattern>\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(2)
	}
	root := flag.Arg(0)

	// Convert path to package pattern and determine Dir for packages.Load
	pattern := "./..."
	dir := root
	if !strings.HasPrefix(root, "./") && !strings.Contains(root, "/") && !strings.Contains(root, ".") {
		pattern = root
		dir = ""
	} else {
		abs, err := filepath.Abs(root)
		if err == nil {
			dir = abs
		}
	}

	fset := token.NewFileSet()
	cfg := &packages.Config{
		Mode:  packages.LoadAllSyntax,
		Fset:  fset,
		Dir:   dir,
		Env:   os.Environ(),
		Tests: false,
	}
	pkgs, err := packages.Load(cfg, pattern)
	if err != nil {
		log.Fatalf("packages.Load: %v", err)
	}
	if packages.PrintErrors(pkgs) > 0 {
		log.Printf("warning: packages had load errors; results may be incomplete")
	}

	// Build SSA program
	prog := ssa.NewProgram(fset, ssa.SanityCheckFunctions)
	var ssaPkgs []*ssa.Package
	for _, pkg := range pkgs {
		if pkg.Types == nil {
			continue
		}
		s := prog.CreatePackage(pkg.Types, pkg.Syntax, pkg.TypesInfo, true)
		ssaPkgs = append(ssaPkgs, s)
	}
	prog.Build()

	// localCounts maps each function to counts found by scanning its instructions.
	type Counts struct{ Entries, Exits, Reads, Writes int }
	localCounts := map[*ssa.Function]Counts{}

	// entryFuncsSet collects functions identified as entry points (main.main and handlers)
	entryFuncsSet := map[*ssa.Function]bool{}

	// Scan all functions to collect local counts and find registrations / main.
	for _, ssaPkg := range ssaPkgs {
		for _, mem := range ssaPkg.Members {
			if fn, ok := mem.(*ssa.Function); ok {
				// identify main.main
				if fn.Pkg != nil && fn.Pkg.Pkg != nil && fn.Pkg.Pkg.Path() == "main" && fn.Name() == "main" {
					entryFuncsSet[fn] = true
				}

				var c Counts
				for _, b := range fn.Blocks {
					for _, instr := range b.Instrs {
						switch ins := instr.(type) {
						case *ssa.Call, *ssa.Defer, *ssa.Go:
							var callCommon *ssa.CallCommon
							switch v := ins.(type) {
							case *ssa.Call:
								callCommon = v.Common()
							case *ssa.Defer:
								callCommon = v.Common()
							case *ssa.Go:
								callCommon = v.Common()
							}
							if callCommon == nil {
								continue
							}
							// Registration detection and handler extraction
							if sc := callCommon.StaticCallee(); sc != nil {
								if isRegistrationFunction(sc) {
									// search args for handler functions or closures
									for i := 0; i < len(callCommon.Args); i++ {
										arg := callCommon.Args[i]
										if hf := extractFunctionFromValue(arg); hf != nil {
											entryFuncsSet[hf] = true
										}
									}
									c.Entries++
								}
							} else {
								// For dynamic call sites we cannot know statically here.
								// Pointer analysis mode will resolve many of these.
							}
							// Count read/write/exit based on static callee if available
							if sc := callCommon.StaticCallee(); sc != nil {
								if matchesExit(sc) {
									c.Exits++
								}
								if matchesRead(sc) {
									c.Reads++
								}
								if matchesWrite(sc) {
									c.Writes++
								}
							}
						}
					}
				}
				localCounts[fn] = c
			}
		}
	}

	// Build the output by traversing from entry functions.
	out := Output{}

	if *ptrMode {
		// Run pointer analysis to build callgraph (resolves interfaces & indirect calls).
		cfg := &pointer.Config{
			Mains: ssaPkgs,
			BuildCallGraph: true,
		}
		res, err := pointer.Analyze(cfg)
		if err != nil {
			log.Fatalf("pointer.Analyze: %v", err)
		}
		cg := res.CallGraph
		// Build mapping from *ssa.Function -> *callgraph.Node
		funcToNode := map[*ssa.Function]*callgraph.Node{}
		for _, n := range cg.Nodes {
			if n.Func != nil {
				funcToNode[n.Func] = n
			}
		}

		for fn := range entryFuncsSet {
			// find callgraph node
			node := funcToNode[fn]
			// if node is nil, fall back to static traversal (we'll handle below)
			if node == nil {
				// fallback static traversal
				pr := traverseStatic(fn, localCounts)
				out.Processes = append(out.Processes, pr)
				out.TotalEntries += pr.Entries
				out.TotalExits += pr.Exits
				out.TotalReads += pr.Reads
				out.TotalWrites += pr.Writes
				continue
			}
			// BFS over callgraph nodes reachable from node
			visited := map[*callgraph.Node]bool{}
			queue := []*callgraph.Node{node}
			pr := ProcessReport{
				Name:   fmt.Sprintf("%s.%s", fn.Pkg.Pkg.Path(), fn.Name()),
				Source: fn.String(),
			}
			for len(queue) > 0 {
				n := queue[0]
				queue = queue[1:]
				if n == nil || visited[n] {
					continue
				}
				visited[n] = true
				if n.Func != nil {
					if lc, ok := localCounts[n.Func]; ok {
						pr.Entries += lc.Entries
						pr.Exits += lc.Exits
						pr.Reads += lc.Reads
						pr.Writes += lc.Writes
					}
					pr.Funcs++
				}
				// enqueue outgoing callees
				for _, e := range n.Out {
					if e == nil || e.Callee == nil {
						continue
					}
					if !visited[e.Callee] {
						queue = append(queue, e.Callee)
					}
				}
			}
			out.Processes = append(out.Processes, pr)
			out.TotalEntries += pr.Entries
			out.TotalExits += pr.Exits
			out.TotalReads += pr.Reads
			out.TotalWrites += pr.Writes
		}
	} else {
		// Non-pointer static traversal (previous behavior)
		for fn := range entryFuncsSet {
			pr := traverseStatic(fn, localCounts)
			out.Processes = append(out.Processes, pr)
			out.TotalEntries += pr.Entries
			out.TotalExits += pr.Exits
			out.TotalReads += pr.Reads
			out.TotalWrites += pr.Writes
		}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		log.Fatalf("encode output: %v", err)
	}
}

// traverseStatic performs a DFS following StaticCallee edges from fn (fallback/static mode).
func traverseStatic(fn *ssa.Function, localCounts map[*ssa.Function]struct{ Entries, Exits, Reads, Writes int }) ProcessReport {
	visited := map[*ssa.Function]bool{}
	stack := []*ssa.Function{fn}
	pr := ProcessReport{
		Name:   fmt.Sprintf("%s.%s", fn.Pkg.Pkg.Path(), fn.Name()),
		Source: fn.String(),
	}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil || visited[n] {
			continue
		}
		visited[n] = true
		if lc, ok := localCounts[n]; ok {
			pr.Entries += lc.Entries
			pr.Exits += lc.Exits
			pr.Reads += lc.Reads
			pr.Writes += lc.Writes
		}
		pr.Funcs++
		// push static callees
		for _, b := range n.Blocks {
			for _, instr := range b.Instrs {
				switch ins := instr.(type) {
				case *ssa.Call, *ssa.Defer, *ssa.Go:
					var callCommon *ssa.CallCommon
					switch v := ins.(type) {
					case *ssa.Call:
						callCommon = v.Common()
					case *ssa.Defer:
						callCommon = v.Common()
					case *ssa.Go:
						callCommon = v.Common()
					}
					if callCommon == nil {
						continue
					}
					if sc := callCommon.StaticCallee(); sc != nil {
						if !visited[sc] {
							stack = append(stack, sc)
						}
					}
				}
			}
		}
	}
	return pr
}

// isRegistrationFunction returns true if the function is a known registration entry point.
func isRegistrationFunction(fn *ssa.Function) bool {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return false
	}
	pkgPath := fn.Pkg.Pkg.Path()
	name := fn.Name()
	if m, ok := entryRegistrations[pkgPath]; ok {
		if m[name] {
			return true
		}
	}
	combined := fmt.Sprintf("%s.%s", pkgPath, name)
	for pk, m := range entryRegistrations {
		for mn := range m {
			if strings.HasSuffix(combined, fmt.Sprintf("%s.%s", pk, mn)) || strings.HasSuffix(name, mn) {
				return true
			}
		}
	}
	return false
}

// matchesRead checks static callee against read function heuristics.
func matchesRead(fn *ssa.Function) bool {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return false
	}
	p := fn.Pkg.Pkg.Path()
	n := fn.Name()
	if set, ok := readFuncs[p]; ok {
		if set[n] {
			return true
		}
	}
	if n == "Read" || n == "Scan" || n == "Query" || n == "QueryRow" {
		return true
	}
	return false
}

// matchesWrite checks static callee against write function heuristics.
func matchesWrite(fn *ssa.Function) bool {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return false
	}
	p := fn.Pkg.Pkg.Path()
	n := fn.Name()
	if set, ok := writeFuncs[p]; ok {
		if set[n] {
			return true
		}
	}
	if n == "Write" || n == "WriteString" || n == "Encode" || n == "Respond" || n == "Print" || n == "Printf" {
		return true
	}
	return false
}

// matchesExit checks static callee against exit heuristics.
func matchesExit(fn *ssa.Function) bool {
	if fn == nil || fn.Pkg == nil || fn.Pkg.Pkg == nil {
		return false
	}
	p := fn.Pkg.Pkg.Path()
	n := fn.Name()
	if set, ok := exitFuncs[p]; ok {
		if set[n] {
			return true
		}
	}
	if p == "os" && n == "Exit" {
		return true
	}
	return false
}

// extractFunctionFromValue attempts to find an *ssa.Function referenced by v.
// It handles direct functions or closures (MakeClosure).
func extractFunctionFromValue(v ssa.Value) *ssa.Function {
	if v == nil {
		return nil
	}
	switch vv := v.(type) {
	case *ssa.MakeClosure:
		if fn, ok := vv.Fn.(*ssa.Function); ok {
			return fn
		}
	case *ssa.Function:
		return vv
	default:
		// not directly resolvable here
	}
	return nil
}
