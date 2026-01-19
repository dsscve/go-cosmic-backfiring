import os
import csv
import subprocess
import json
from concurrent.futures import ThreadPoolExecutor, as_completed
from github import Github
from rich.progress import Progress

# ---------------- CONFIGURATION ----------------
GITHUB_TOKEN = os.getenv("GITHUB_TOKEN")
TOP_N = 50
MAX_WORKERS = 6
BASE_DIR = "data/go_repos"
RESULTS_FILE = "results/go_eloc_fp.csv"
GO_AST_BIN = "./go_cosmic_ast"

# ---------------- FETCH TOP REPOS ----------------
def fetch_top_go_repos(top_n=TOP_N):
    if not GITHUB_TOKEN:
        raise Exception("Set GITHUB_TOKEN environment variable")
    g = Github(GITHUB_TOKEN)
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
            stderr=subprocess.DEVNULL
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

# ---------------- ANALYSIS ----------------
def count_tokei_eloc(repo_path):
    try:
        result = subprocess.run(
            ["tokei", repo_path, "--type", "Go", "--output", "json"],
            capture_output=True, text=True, check=True
        )
        data = json.loads(result.stdout)
        return data.get("Go", {}).get("code", 0)
    except Exception as e:
        print(f"[WARN] Tokei failed for {repo_path}: {e}")
        return 0

def count_cosmic_fp_ast(repo_path):
    try:
        result = subprocess.run(
            [GO_AST_BIN, repo_path],
            capture_output=True, text=True, check=True
        )
        data = json.loads(result.stdout)
        entries = data.get("Entries", 0)
        exits = data.get("Exits", 0)
        reads = data.get("Reads", 0)
        writes = data.get("Writes", 0)
        total_fp = entries + exits + reads + writes
        return entries, exits, reads, writes, total_fp
    except Exception as e:
        print(f"[WARN] AST FP failed for {repo_path}: {e}")
        return 0, 0, 0, 0, 0

def analyze_repo(repo_path):
    total_loc = count_tokei_eloc(repo_path)
    e, x, r, w, fp = count_cosmic_fp_ast(repo_path)
    eloc_per_fp = total_loc / fp if fp else 0

    return {
        "repo": os.path.basename(repo_path),
        "total_loc": total_loc,
        "entries": e,
        "exits": x,
        "reads": r,
        "writes": w,
        "cosmic_fp": fp,
        "eloc_per_fp": eloc_per_fp
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
        "repo", "total_loc",
        "entries", "exits", "reads", "writes",
        "cosmic_fp", "eloc_per_fp"
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

    print("🔨 Building Go AST analyzer...")
    subprocess.run(
        ["go", "build", "-o", GO_AST_BIN, "go_cosmic_ast.go"],
        check=True
    )

    print("📊 Analyzing repositories...")
    results = analyze_repos_parallel(repo_paths)
    print(f"✅ Analysis complete! Results saved to {RESULTS_FILE}")

    valid = [r["eloc_per_fp"] for r in results if r["eloc_per_fp"] > 0]
    if valid:
        avg = sum(valid) / len(valid)
        print(f"\n💡 Average eLOC/FP across {len(valid)} Go repos: {avg:.2f}")
    else:
        print("\n⚠️ No valid COSMIC FP found.")

if __name__ == "__main__":
    main()
