import os
import csv
import json
import subprocess
from concurrent.futures import ThreadPoolExecutor, as_completed
from github import Github, Auth
from rich.progress import Progress

# ---------------- CONFIGURATION ----------------
GITHUB_TOKEN = os.getenv("GITHUB_TOKEN")
TOP_N = 50
MAX_WORKERS = 8
BASE_DIR = "data/go_repos"
RESULTS_FILE = "results/go_eloc_fp.csv"
AST_BINARY = "./go_cosmic_ast"

# ---------------- FETCH TOP REPOS ----------------
def fetch_top_go_repos(top_n=TOP_N):
    if not GITHUB_TOKEN:
        raise Exception("Set GITHUB_TOKEN environment variable")

    auth = Auth.Token(GITHUB_TOKEN)
    g = Github(auth=auth)

    query = "language:Go stars:>1000"
    result = g.search_repositories(query=query, sort="stars", order="desc")

    repos = []
    for repo in result[:top_n]:
        repos.append({
            "name": repo.full_name,
            "clone_url": repo.clone_url,
            "stars": repo.stargazers_count,
            "forks": repo.forks_count
        })
    return repos

# ---------------- CLONE REPOS ----------------
def clone_repo(repo):
    os.makedirs(BASE_DIR, exist_ok=True)
    repo_path = os.path.join(BASE_DIR, repo["name"].replace("/", "_"))

    if os.path.exists(repo_path):
        return repo_path

    try:
        subprocess.run(
            ["git", "clone", "--depth", "1", repo["clone_url"], repo_path],
            check=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
        )
        return repo_path
    except Exception as e:
        print(f"[ERROR] Cloning {repo['name']}: {e}")
        return None

def clone_repos_parallel(repos):
    paths = []
    with ThreadPoolExecutor(max_workers=MAX_WORKERS) as executor:
        futures = {executor.submit(clone_repo, r): r for r in repos}
        with Progress() as progress:
            task = progress.add_task("[cyan]Cloning repos...", total=len(futures))
            for f in as_completed(futures):
                path = f.result()
                if path:
                    paths.append(path)
                progress.update(task, advance=1)
    return paths

# ---------------- BUILD GO AST ANALYZER ----------------
def build_ast_analyzer():
    if os.path.exists(AST_BINARY):
        return

    print("🔨 Building Go AST analyzer...")

    go_source = r'''
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
'''

    with open("go_cosmic_ast.go", "w", encoding="utf-8") as f:
        f.write(go_source)

    subprocess.run(["go", "build", "-o", "go_cosmic_ast", "go_cosmic_ast.go"], check=True)

# ---------------- TOKEI ELOC ----------------
def get_eloc_with_tokei(repo_path):
    try:
        output = subprocess.check_output(
            ["tokei", "--output", "json", repo_path],
            stderr=subprocess.DEVNULL,
        ).decode("utf-8")

        data = json.loads(output)
        go_data = data.get("Go")
        if not go_data:
            return 0

        return go_data["code"]
    except Exception as e:
        print(f"[WARN] Tokei failed on {repo_path}: {e}")
        return 0

# ---------------- RUN AST ANALYZER ----------------
def run_ast_analyzer(repo_path):
    try:
        output = subprocess.check_output(
            [AST_BINARY, repo_path],
            stderr=subprocess.DEVNULL,
        ).decode("utf-8")

        data = json.loads(output)
        return (
            data.get("entries", 0),
            data.get("exits", 0),
            data.get("reads", 0),
            data.get("writes", 0),
        )
    except Exception as e:
        print(f"[WARN] AST failed on {repo_path}: {e}")
        return 0, 0, 0, 0

# ---------------- ANALYSIS ----------------
def analyze_repo(repo_path):
    eloc = get_eloc_with_tokei(repo_path)
    entries, exits, reads, writes = run_ast_analyzer(repo_path)

    total_fp = entries + exits + reads + writes
    eloc_per_fp = eloc / total_fp if total_fp else 0

    return {
        "repo": os.path.basename(repo_path),
        "total_loc": eloc,
        "entries": entries,
        "exits": exits,
        "reads": reads,
        "writes": writes,
        "cosmic_fp": total_fp,
        "eloc_per_fp": round(eloc_per_fp, 2),
    }

def analyze_repos_parallel(repo_paths):
    results = []

    with ThreadPoolExecutor(max_workers=MAX_WORKERS) as executor:
        futures = {executor.submit(analyze_repo, p): p for p in repo_paths}

        with Progress() as progress:
            task = progress.add_task("[green]Analyzing repos...", total=len(futures))

            for f in as_completed(futures):
                result = f.result()
                results.append(result)
                progress.update(task, advance=1)

    os.makedirs("results", exist_ok=True)

    fieldnames = [
        "repo",
        "total_loc",
        "entries",
        "exits",
        "reads",
        "writes",
        "cosmic_fp",
        "eloc_per_fp",
    ]

    with open(RESULTS_FILE, "w", newline="", encoding="utf-8") as csvfile:
        writer = csv.DictWriter(csvfile, fieldnames=fieldnames)
        writer.writeheader()
        for r in results:
            writer.writerow(r)

    return results

# ---------------- MAIN ----------------
def main():
    print("🚀 Fetching top Go repos...")
    repos = fetch_top_go_repos(TOP_N)
    print(f"Found {len(repos)} repositories.")

    print("📥 Cloning repositories...")
    repo_paths = clone_repos_parallel(repos)
    print(f"Cloned {len(repo_paths)} repositories.")

    build_ast_analyzer()

    print("📊 Analyzing repositories...")
    results = analyze_repos_parallel(repo_paths)

    print(f"✅ Analysis complete! Results saved to {RESULTS_FILE}")

    valid = [r["eloc_per_fp"] for r in results if r["eloc_per_fp"] > 0]

    if valid:
        avg = sum(valid) / len(valid)
        print(f"\n💡 Average eLOC/FP across {len(valid)} repos: {avg:.2f}")
    else:
        print("\n⚠️ No valid COSMIC FP found.")

if __name__ == "__main__":
    main()
