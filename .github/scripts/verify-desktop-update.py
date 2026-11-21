import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import plistlib
import shutil
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from zipfile import ZipFile

PROJECT = Path(__file__).resolve().parents[2]
SYSTEM = platform.system()
FIXTURE_VERSION = "1.0.99"  # An update-capable test build, never the published v1.0 client.
BINARY = "klei-points-auto-claim" + (".exe" if SYSTEM == "Windows" else "")
MAC_TASK = "com.kleirewardhelper.background"
LATEST = "/repos/fixture/desktop/releases/latest"


def run(*args, env=None, cwd=None):
    return subprocess.run(args, env=env, cwd=cwd, check=True, text=True,
                          stdout=subprocess.PIPE, timeout=120).stdout.strip()


def require(condition, message):
    if not condition:
        raise RuntimeError(message)


def wait_for(description, predicate):
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.4)
    raise TimeoutError(description)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


class ReleaseFeed(BaseHTTPRequestHandler):
    def do_GET(self):
        server = self.server
        if self.path == LATEST:
            release = server.release if server.available else {"tag_name": "v" + FIXTURE_VERSION}
            self.respond(json.dumps(release).encode())
        elif self.path == "/" + server.archive.name and server.available:
            self.respond(server.archive.read_bytes())
        elif self.path == "/SHA256SUMS" and server.available:
            self.respond(server.archive.with_name("SHA256SUMS").read_bytes())
        else:
            self.send_error(404)

    def respond(self, data):
        self.send_response(200)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *args):
        pass


def release_feed(archive, version, test_manifest):
    server = ThreadingHTTPServer(("127.0.0.1", 0), ReleaseFeed)
    origin = "http://127.0.0.1:" + str(server.server_port)
    asset = {"name": archive.name, "size": archive.stat().st_size,
             "browser_download_url": origin + "/" + archive.name}
    assets = [asset]
    if test_manifest:
        manifest = archive.with_name("SHA256SUMS")
        assets.append({"name": manifest.name, "size": manifest.stat().st_size,
                       "browser_download_url": origin + "/" + manifest.name})
    else:
        asset["digest"] = "sha256:" + digest(archive)
    server.archive = archive
    server.available = False
    server.release = {"tag_name": "v" + version, "draft": False, "prerelease": False, "assets": assets}
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server


def core(folder):
    if SYSTEM == "Darwin":
        return next(folder.glob("*.app")) / "Contents/Helpers/klei-core"
    return folder / BINARY


def make_fixture(folder, archive, latest, version):
    folder.mkdir()
    if SYSTEM == "Darwin":
        run("ditto", "-x", "-k", archive, folder)
    else:
        with ZipFile(archive) as package:
            package.extractall(folder)
    exe = core(folder)
    exe.chmod(0o700)
    expected = digest(exe)
    require(run(exe, "--version") == version, "Archive version mismatch")
    flags = f"-s -w -X main.releaseVersion={FIXTURE_VERSION} -X main.latestReleaseURL={latest}"
    if SYSTEM == "Windows":
        flags += " -H=windowsgui"
    run("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", flags, "-o", exe, ".",
        cwd=PROJECT / "desktop/core", env={**os.environ, "CGO_ENABLED": "0"})
    if SYSTEM == "Darwin":
        app = next(folder.glob("*.app"))
        run("plutil", "-replace", "CFBundleShortVersionString", "-string", FIXTURE_VERSION, app / "Contents/Info.plist")
        run("codesign", "--force", "--sign", "-", "--timestamp=none", "--identifier", "local.klei-reward-check.klei-core", exe)
        run("codesign", "--force", "--sign", "-", "--timestamp=none", app)
    require(run(exe, "--version") == FIXTURE_VERSION, "Older fixture did not start")
    return expected


def seed_profile(root, enabled):
    monitor = root / "monitor-klei"
    root.mkdir(mode=0o700)
    monitor.mkdir(mode=0o700)
    history = {"version": 1, "active": None, "seen": {}, "credited_at": 1700000000,
               "stop": {"status": "VERIFIED_POINTS", "reward": "https://accounts.klei.com/link/fixture",
                        "points_before": 10, "points_after": 60, "points_added": 50}}
    saved = {
        root / "klei.cookies": b"accounts.klei.com\tFALSE\t/\tTRUE\t4102444800\tsession\tsynthetic-fixture\n",
        root / "klei.reward-verification.json": json.dumps(history).encode(),
        monitor / "automatic.json": json.dumps({"enabled": enabled, "include_gifts": True, "revision": 5}).encode(),
        monitor / "appearance.json": b'"dark"',
    }
    for path, content in saved.items():
        path.write_bytes(content)
        path.chmod(0o600)
    return saved


def active_worker(root):
    marker = root / "monitor-klei/native-worker.json"
    if marker.exists():
        worker_id = json.loads(marker.read_bytes())
        name = "klei-worker.exe" if SYSTEM == "Windows" else "klei-worker"
        return root / "bin" / worker_id / name


def verify_registration(worker, env):
    if SYSTEM == "Darwin":
        task = run("launchctl", "print", f"gui/{os.getuid()}/{MAC_TASK}", env=env)
    elif SYSTEM == "Windows":
        task = run("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
                   "$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value; "
                   "$task = Get-ScheduledTask -TaskName ('KleiRewardHelper-' + $sid) -ErrorAction Stop; "
                   "if ($task.State -eq 'Disabled') { exit 1 }; $task.Actions.Execute; $task.Actions.Arguments", env=env)
    else:
        require(run("systemctl", "--user", "is-active", "klei-reward-helper.timer", env=env) == "active",
                "Updated timer is inactive")
        task = run("systemctl", "--user", "show", "-p", "ExecStart", "--value", "klei-reward-helper.service", env=env)
    require(str(worker) in task and "--run" in task, f"Scheduler points elsewhere: {task}")


def run_scheduled_worker(worker, root, env):
    # A concurrent scan holds this lock. The real --run entry skips reward traffic
    # and still checks for an update. Closing the file lets the Mac helper re-register.
    with (root / "monitor-klei/background.lock").open("a+b") as lock:
        if SYSTEM == "Windows":
            import msvcrt
            lock.seek(0)
            msvcrt.locking(lock.fileno(), msvcrt.LK_NBLCK, 1)
        else:
            import fcntl
            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
        run(worker, "--run", env=env)


def verify_upgrade(work, template, server, expected, version, scheduled):
    home = work / "profile"
    home.mkdir(parents=True)
    env = {**os.environ, "HOME": str(home), "LOCALAPPDATA": str(home)}
    if SYSTEM == "Linux" and scheduled:
        # The running systemd user manager resolves unit names under its own config directory.
        env["XDG_CONFIG_HOME"] = os.environ.get("XDG_CONFIG_HOME", str(Path.home() / ".config"))
    root = home / ("KleiRewardCheck" if SYSTEM == "Windows" else ".klei-reward-check")
    saved = seed_profile(root, scheduled)
    # Exercise ordinary install paths containing spaces.
    installed = work / "Desktop app"
    shutil.copytree(template, installed)
    exe = core(installed)
    server.available = not scheduled
    try:
        view = json.loads(run(exe, "--open-status", env=env))
        require(view["saved"] and view["enabled"] == scheduled, "Old fixture could not read preferences/session")
        if scheduled:
            old = active_worker(root)
            require(old and run(old, "--version", env=env) == FIXTURE_VERSION, "Old worker not registered")
            verify_registration(old, env)
            # Simulate the next scheduled check after a day without a new release.
            check = root / "monitor-klei/update-check.json"
            wait_for("First check did not complete", check.exists)
            state = json.loads(check.read_bytes())
            state["checked"] = "2000-01-01T00:00:00Z"
            check.write_text(json.dumps(state))
            server.available = True
            run_scheduled_worker(old, root, env)
        wait_for("Original install path never acquired the published binary",
                 lambda: exe.exists() and digest(exe) == expected)
        require(run(exe, "--version", env=env) == version, "Updated client returned a wrong version")
        if SYSTEM == "Darwin":
            app = next(installed.glob("*.app"))
            run("codesign", "--verify", "--strict", "--deep", app)
            require(run("plutil", "-extract", "CFBundleShortVersionString", "raw", app / "Contents/Info.plist") == version,
                    "The application bundle was not replaced")
        if scheduled:
            def worker_migrated():
                dest = active_worker(root)
                return dest and dest != old and dest.is_file() and digest(dest) == expected
            wait_for("The enabled task kept using the old worker", worker_migrated)
            worker = active_worker(root)
            verify_registration(worker, env)
            require(run(worker, "--version", env=env) == version, "Scheduled worker returned an old version")
        else:
            require(active_worker(root) is None, "Opening the disabled profile enabled a task")
        for path, content in saved.items():
            require(path.read_bytes() == content, f"Upgrade changed {path.name}")
        view = json.loads(run(exe, "--status", env=env))
        require(view["saved"] and view["enabled"] == scheduled and view["include_gifts"] and
                view["revision"] == 5 and any("+50" in item for item in view["receipts"]), "Updated profile was unreadable")
        mode = "scheduled worker" if scheduled else "panel with auto-claim disabled"
        print(f"{mode}: {FIXTURE_VERSION} -> {version}, installed SHA-256 {expected}, profile preserved", flush=True)
    finally:
        if scheduled:
            run(exe, "--action", "stop", "0", env=env)
            helper = root / "update-helper.plist"
            if SYSTEM == "Darwin" and helper.exists():
                run("launchctl", "bootout", f"gui/{os.getuid()}", helper, env=env)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("archives", type=Path, help="release ZIP or directory of all release archives")
    parser.add_argument("--verify-schedule", action="store_true", help="also register and verify an actual task on a GitHub-hosted runner")
    args = parser.parse_args()
    if args.verify_schedule:
        require(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("RUNNER_ENVIRONMENT") == "github-hosted",
                "The scheduling test requires an ephemeral GitHub-hosted runner")
    os_name = {"Darwin": "macOS", "Linux": "Linux", "Windows": "Windows"}[SYSTEM]
    arch = {"amd64": "x64", "x86_64": "x64", "arm64": "arm64", "aarch64": "arm64"}[platform.machine().lower()]
    archive_name = f"Klei-Points-Auto-Claim-{os_name}-{arch}.zip"
    archive = args.archives.resolve()
    if archive.is_dir():
        archive = archive / archive_name
    require(archive.name == archive_name and archive.is_file(), f"Expected native download {archive_name}")
    with (PROJECT / "desktop/macos/Info.plist").open("rb") as source:
        version = plistlib.load(source)["CFBundleShortVersionString"]
    server = release_feed(archive, version, args.verify_schedule and SYSTEM == "Linux")
    try:
        with tempfile.TemporaryDirectory(prefix="klei-upgrade-") as tmp:
            base = Path(tmp).resolve()
            template = base / "fixture"
            latest = f"http://127.0.0.1:{server.server_port}{LATEST}"
            expected = make_fixture(template, archive, latest, version)
            verify_upgrade(base / "panel", template, server, expected, version, False)
            if args.verify_schedule:
                verify_upgrade(base / "worker", template, server, expected, version, True)
    finally:
        server.shutdown()
        server.server_close()


if __name__ == "__main__":
    main()
