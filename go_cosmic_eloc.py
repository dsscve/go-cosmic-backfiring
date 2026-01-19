import os
import re
import csv
import subprocess
from concurrent.futures import ThreadPoolExecutor, as_completed
from github import Github
from rich.progress import Progress

# ---------------- CONFIGURATION ----------------
GITHUB_TOKEN = os.getenv("GITHUB_TOKEN")
TOP_N = 50
MAX_WORKERS = 8
BASE_DIR = "data/go_repos"
RESULTS_FILE = "results/go_eloc_fp.csv"  # <-- CSV output

# ---------------- COSMIC HEURISTIC ----------------
COMMENT_PATTERN = re.compile(r'^\s*//|^\s*/\*|^\s*\*/|^\s*\*')
DATA_MOVEMENT_KEYWORDS = {
    "entry": ["func", "interface"],
    "exit": ["return"],
    "read": ["Read", "Scan", "os.Open"],
    "write": ["Write", "Print", "os.Create"]
}

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
            check=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
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
def count_effective_loc(file_path):
    loc = 0
    try:
        with open(file_path, 'r', encoding='utf-8', errors='ignore') as f:
            for line in f:
                line = line.strip()
                if line and not COMMENT_PATTERN.match(line):
                    loc += 1
    except Exception as e:
        print(f"[WARN] Reading {file_path}: {e}")
    return loc

def count_cosmic_fp(file_path):
    entries = exits = reads = writes = 0
    try:
        with open(file_path, 'r', encoding='utf-8', errors='ignore') as f:
            for line in f:
                l = line.strip()
                for keyword in DATA_MOVEMENT_KEYWORDS["entry"]:
                    if keyword in l: entries += 1
                for keyword in DATA_MOVEMENT_KEYWORDS["exit"]:
                    if keyword in l: exits += 1
                for keyword in DATA_MOVEMENT_KEYWORDS["read"]:
                    if keyword in l: reads += 1
                for keyword in DATA_MOVEMENT_KEYWORDS["write"]:
                    if keyword in l: writes += 1
    except Exception as e:
        print(f"[WARN] FP analysis {file_path}: {e}")
    total_fp = entries + exits + reads + writes
    return entries, exits, reads, writes, total_fp

def analyze_repo(repo_path):
    total_loc = total_entries = total_exits = total_reads = total_writes = total_fp = 0
    for root, dirs, files in os.walk(repo_path):
        for file in files:
            if file.endswith(".go"):
                file_path = os.path.join(root, file)
                loc = count_effective_loc(file_path)
                e, x, r, w, fp = count_cosmic_fp(file_path)
                total_loc += loc
                total_entries += e
                total_exits += x
                total_reads += r
                total_writes += w
                total_fp += fp
    eloc_per_fp = total_loc / total_fp if total_fp else 0
    return {
        "repo": os.path.basename(repo_path),
        "total_loc": total_loc,
        "entries": total_entries,
        "exits": total_exits,
        "reads": total_reads,
        "writes": total_writes,
        "cosmic_fp": total_fp,
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

    # Ensure results folder exists
    os.makedirs("results", exist_ok=True)

    # Write CSV
    fieldnames = ["repo", "total_loc", "entries", "exits", "reads", "writes", "cosmic_fp", "eloc_per_fp"]
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

    print("📥 Cloning repositories (ephemeral in GitHub Actions)...")
    repo_paths = clone_repos_parallel(repos)
    print(f"Cloned {len(repo_paths)} repositories.")

    print("📊 Analyzing repositories...")
    results = analyze_repos_parallel(repo_paths)
    print(f"✅ Analysis complete! Results saved to {RESULTS_FILE}")

    # Print average eLOC/FP
    valid_eloc_fp = [r['eloc_per_fp'] for r in results if r['eloc_per_fp'] > 0]
    if valid_eloc_fp:
        avg_eloc_fp = sum(valid_eloc_fp) / len(valid_eloc_fp)
        print(f"\n💡 Average eLOC/FP across {len(valid_eloc_fp)} Go repos: {avg_eloc_fp:.2f}")
    else:
        print("\n⚠️ No valid COSMIC FP found.")

if __name__ == "__main__":
    main()
