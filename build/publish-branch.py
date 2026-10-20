#!/usr/bin/env python3
"""Publish the local beta branch with author and committer dates set at publication."""
import argparse
from datetime import datetime, timezone
import os
from pathlib import Path
import subprocess


def git(*args, data=None, env=None):
    return subprocess.check_output(
        ["git", "-c", f"safe.directory={Path.cwd().as_posix()}", *args],
        input=data, env=env,
    )


def rewrite(base, tip, date):
    """Create replacement objects without changing any refs or working files."""
    commits = git("rev-list", "--reverse", "--topo-order", f"{base}..{tip}").split()
    mapped = {}
    for old in commits:
        header, message = git("cat-file", "commit", old.decode()).split(b"\n\n", 1)
        fields = header.splitlines()
        tree = next(x[5:] for x in fields if x.startswith(b"tree "))
        parents = [x[7:] for x in fields if x.startswith(b"parent ")]
        env = os.environ.copy()
        for role in ("author", "committer"):
            identity = next(x[len(role)+1:] for x in fields if x.startswith(role.encode()+b" "))
            name, rest = identity.rsplit(b" <", 1)
            email = rest.split(b">", 1)[0]
            env[f"GIT_{role.upper()}_NAME"] = name.decode("utf-8")
            env[f"GIT_{role.upper()}_EMAIL"] = email.decode("utf-8")
            env[f"GIT_{role.upper()}_DATE"] = date
        args = ["commit-tree", tree.decode()]
        for parent in parents:
            args += ["-p", mapped.get(parent, parent).decode()]
        mapped[old] = git(*args, data=message, env=env).strip()
    return mapped.get(tip.encode(), tip.encode()).decode(), len(commits)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--publish", action="store_true", help="rewrite dates and push; default is preview only")
    args = parser.parse_args()
    os.chdir(Path(__file__).resolve().parents[1])
    branch = git("branch", "--show-current").decode().strip()
    if branch != "macos-beta":
        parser.error("check out macos-beta first")
    if git("status", "--porcelain", "--untracked-files=no").strip():
        parser.error("commit or stash tracked changes first")
    base = git("rev-parse", "main").decode().strip()
    tip = git("rev-parse", "HEAD").decode().strip()
    git("merge-base", "--is-ancestor", base, tip)
    count = int(git("rev-list", "--count", f"{base}..{tip}"))
    if not count:
        parser.error("no branch commits to publish")
    print(f"{count} commits after main; both date fields will use the publication time.", flush=True)
    if not args.publish:
        print("Preview only; no refs, timestamps or remote state changed. Use --publish when ready.")
        return
    ref = f"refs/heads/{branch}"
    if git("ls-remote", "--heads", "origin", ref).strip():
        parser.error("remote macos-beta already exists; refusing to rewrite shared history")
    now = datetime.now(timezone.utc)
    date = now.strftime("%Y-%m-%dT%H:%M:%S +0000")
    new_tip, _ = rewrite(base, tip, date)
    backup = f"refs/backups/publication/{now.strftime('%Y%m%dT%H%M%S%fZ')}"
    git("update-ref", backup, tip)
    git("update-ref", ref, new_tip, tip)
    print(f"Original tip saved as {backup}; new commit dates: {date}", flush=True)
    git("push", "--set-upstream", f"--force-with-lease={ref}:", "origin", f"{ref}:{ref}")
    print(f"Published {branch} at {new_tip}. Wait for native CI before tagging a 0.2.0 release.")


if __name__ == "__main__":
    main()
