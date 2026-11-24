"""Measure a command on its Linux host. Pass existing paths on each filesystem."""
import argparse
from contextlib import ExitStack
import json
import os
from pathlib import Path
import statistics
import subprocess
import time


def used_memory():
    fields = {}
    for line in Path("/proc/meminfo").read_text().splitlines():
        key, value = line.split(":", 1)
        if key in ("MemTotal", "MemAvailable"):
            fields[key] = int(value.split()[0]) * 1024
    return fields["MemTotal"] - fields["MemAvailable"]


def available(fd):
    stats = os.fstatvfs(fd)
    return stats.f_bavail * stats.f_frsize


def measure(paths, command):
    with ExitStack() as opened:
        filesystems = {}
        descriptors = {}
        for path in paths:
            fd = os.open(path, os.O_RDONLY)
            opened.callback(os.close, fd)
            device = os.fstat(fd).st_dev
            if device not in descriptors:
                descriptors[device] = fd
                free = available(fd)
                filesystems[fd] = dict(paths=[], before=free, minimum=free)
            filesystems[descriptors[device]]["paths"].append(path)
        return sample(filesystems, command)


def sample(filesystems, command):
    memory = [used_memory()]
    started = time.monotonic()
    with subprocess.Popen(command) as process:
        while True:
            try:
                status = process.wait(timeout=0.1)
            except subprocess.TimeoutExpired:
                status = None
            elapsed = time.monotonic() - started
            memory.append(used_memory())
            for fd, entry in filesystems.items():
                free = available(fd)
                entry.update(minimum=min(entry["minimum"], free), after=free)
            if status is not None:
                break
    for entry in filesystems.values():
        entry["peak_additional_bytes"] = entry["before"] - entry["minimum"]
    return dict(exit_code=status, elapsed_seconds=elapsed,
                samples=len(memory), median_used_ram_bytes=statistics.median(memory),
                peak_used_ram_bytes=max(memory), filesystem_free_bytes=list(filesystems.values()))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--path", action="append", required=True)
    parser.add_argument("--receipt", required=True)
    parser.add_argument("command", nargs="+")
    args = parser.parse_args()
    receipt = Path(args.receipt)
    output = receipt.open("x")
    try:
        with output:
            result = measure(args.path, args.command)
            json.dump(result, output, indent=2)
            output.write("\n")
    except BaseException:
        receipt.unlink(missing_ok=True)
        raise
    raise SystemExit(result["exit_code"] or 0)
