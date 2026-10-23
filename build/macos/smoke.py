"""Launch the extracted, relocated .app; report immediate native/WebKit startup failures."""
import os
from pathlib import Path
import subprocess
import sys
import time

app = Path(sys.argv[1]).resolve()
binary = app / "Contents/MacOS/braw-pack-gui"
env = dict(os.environ, PATH="/usr/bin:/bin:/usr/sbin:/sbin")
with open("macos-gui-smoke.log", "w+") as log:
    proc = subprocess.Popen([str(binary)], cwd="/", env=env, stdout=log, stderr=log)
    try:
        time.sleep(10)
        if proc.poll() is not None:
            log.seek(0)
            raise RuntimeError(f"GUI exited during startup ({proc.returncode}): {log.read()}")
        assert not (app / "Contents/MacOS/braw-pack-data").exists()
        print("GUI stayed running for 10 seconds from relocated .app without Homebrew on PATH.")
    finally:
        if proc.poll() is None:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
subprocess.run(["codesign", "--verify", "--deep", "--strict", str(app)], check=True)
