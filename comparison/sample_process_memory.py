"""Sample RSS of one owned application process and its descendants (KiB)."""
import argparse
import json
import subprocess
import time


def process_tree(rows, root):
    processes = {int(pid): (int(parent), int(rss)) for pid, parent, rss in rows}
    if root not in processes:
        return None
    selected = {root}
    while True:
        children = {pid for pid, (parent, _) in processes.items() if parent in selected}
        if children <= selected:
            break
        selected |= children
    return {pid: processes[pid][1] for pid in selected}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("pid", type=int)
    args = parser.parse_args()
    while True:
        output = subprocess.check_output(["ps", "-axo", "pid=,ppid=,rss="], text=True)
        tree = process_tree([line.split() for line in output.splitlines()], args.pid)
        if tree is None:
            break
        print(json.dumps({"time_unix": time.time(), "rss_kib": sum(tree.values()),
                          "processes_rss_kib": tree}), flush=True)
        time.sleep(0.25)


if __name__ == "__main__":
    main()
