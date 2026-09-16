"""Copy legacy volumes; sources are mounted read-only by Echo, never followed."""
import filecmp
import json
import os
from pathlib import Path
import shutil
import stat
import tempfile


def remove_entry(path):
    if path.is_symlink() or not path.is_dir():
        path.unlink()
    else:
        shutil.rmtree(path)


def copy_entry(source, target):
    info = source.lstat()
    if stat.S_ISLNK(info.st_mode):
        target.symlink_to(os.readlink(source))
    elif stat.S_ISDIR(info.st_mode):
        target.mkdir(exist_ok=True)
        for child in sorted(source.iterdir()):
            copy_entry(child, target / child.name)
    elif stat.S_ISREG(info.st_mode):
        shutil.copyfile(source, target, follow_symlinks=False)
    else:
        return  # Live sockets, devices, and FIFOs are not persistent data.
    shutil.copystat(source, target, follow_symlinks=False)
    if hasattr(os, "lchown"):
        os.lchown(target, info.st_uid, info.st_gid)


def same_entry(a, b):
    left, right = a.lstat(), b.lstat()
    if (left.st_mode, left.st_uid, left.st_gid) != (right.st_mode, right.st_uid, right.st_gid):
        return False
    if a.is_symlink():
        return b.is_symlink() and os.readlink(a) == os.readlink(b)
    return a.is_file() and b.is_file() and filecmp.cmp(a, b, shallow=False)


def bytes_in(path):
    info = path.lstat()
    if stat.S_ISDIR(info.st_mode):
        return sum(bytes_in(child) for child in path.iterdir())
    return info.st_size if stat.S_ISREG(info.st_mode) else 0


def migrate(source_root, target_root, preflight=False):
    source_root, target_root = Path(source_root), Path(target_root)
    names = ("workbench", "desktop", "browser", "exchange")
    for name in names:
        if not (source_root / name).is_dir() or (source_root / name).is_symlink():
            raise RuntimeError("Missing legacy volume: " + name)
    # Worst case includes both archived versions of each home conflict.
    needed = 3 * (bytes_in(source_root / "workbench") + bytes_in(source_root / "desktop"))
    needed += bytes_in(source_root / "browser") + bytes_in(source_root / "exchange") + (64 << 20)
    if shutil.disk_usage(target_root / "home").free < needed:
        raise RuntimeError("Insufficient space for recoverable sandbox migration")
    if preflight:
        return
    for name in ("home", "browser", "exchange"):
        destination = target_root / name
        if destination.is_symlink() or not destination.is_dir():
            raise RuntimeError("Invalid migration destination")
        for entry in destination.iterdir():
            remove_entry(entry)
    copy_entry(source_root / "workbench", target_root / "home")
    archive = Path(tempfile.mkdtemp(prefix="sandbox-migration-conflicts-", dir=target_root / "home"))
    conflicts = []
    owner = (target_root / "home").stat()
    os.chown(archive, owner.st_uid, owner.st_gid)

    def archive_parents(path):
        if path == archive or path.exists():
            return
        archive_parents(path.parent)
        path.mkdir()
        os.chown(path, owner.st_uid, owner.st_gid)
    desktop_settings = (".config/xfce4", ".config/gtk-3.0", ".config/Thunar", ".local/share/keyrings")

    def winner_for(key, source):
        if any(key == prefix or key.startswith(prefix + "/") for prefix in desktop_settings):
            return "desktop"
        # A directory replacing a baseline file/symlink may contain the active
        # desktop settings. Archive the baseline before using that directory.
        if source.is_dir() and not source.is_symlink() and any(prefix.startswith(key + "/") for prefix in desktop_settings):
            return "desktop"
        return "workbench"

    def archive_conflict(source, target, relative, winner):
        for role, existing in (("workbench", target), ("desktop", source)):
            saved = archive / role / relative
            if not os.path.lexists(saved):
                archive_parents(saved.parent)
                copy_entry(existing, saved)
        conflicts.append({"path": relative.as_posix(), "active": winner})

    def merge(source, target, relative):
        if not os.path.lexists(target):
            copy_entry(source, target)
            return
        key = relative.as_posix()
        if source.is_dir() and not source.is_symlink() and target.is_dir() and not target.is_symlink():
            left, right = source.lstat(), target.lstat()
            different_metadata = (left.st_mode, left.st_uid, left.st_gid) != (right.st_mode, right.st_uid, right.st_gid)
            winner = "desktop" if any(key == prefix or key.startswith(prefix + "/") for prefix in desktop_settings) else "workbench"
            if different_metadata:
                archive_conflict(source, target, relative, winner)
            for entry in sorted(source.iterdir()):
                merge(entry, target / entry.name, relative / entry.name)
            if different_metadata and winner == "desktop":
                shutil.copystat(source, target, follow_symlinks=False)
                os.lchown(target, left.st_uid, left.st_gid)
            return
        if same_entry(source, target):
            return
        winner = winner_for(key, source)
        archive_conflict(source, target, relative, winner)
        if winner == "desktop":
            remove_entry(target)
            copy_entry(source, target)

    for entry in sorted((source_root / "desktop").iterdir()):
        merge(entry, target_root / "home" / entry.name, Path(entry.name))
    for name in ("browser", "exchange"):
        copy_entry(source_root / name, target_root / name)
    # Chromium's process locks name a container that is now stopped.
    for name in ("SingletonLock", "SingletonCookie", "SingletonSocket"):
        lock = target_root / "browser" / name
        if lock.is_symlink() or lock.is_file():
            lock.unlink()
    (archive / "report.json").write_text(json.dumps(conflicts, indent=2) + "\n", encoding="utf-8")
    os.chown(archive / "report.json", owner.st_uid, owner.st_gid)
    print(json.dumps({"conflicts": len(conflicts), "report": str(archive / "report.json")}))


if __name__ == "__main__":
    migrate("/source", "/target", os.environ.get("ECHO_MIGRATION_PREFLIGHT") == "true")
