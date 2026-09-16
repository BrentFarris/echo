"""Install pinned P4 2024.1 and Fossil 2.28 for isolated SCM CI fixtures."""
import hashlib
import io
import os
from pathlib import Path
import platform
import tempfile
import tarfile
import urllib.request
import zipfile

PINS = {
    "Windows": ("bin.ntx64", {
        "p4.exe": "3f84348f97b4fc5e1d52ce30e52edd552e6abac7a5f60de9d98b355e8edd3216",
        "p4d.exe": "9bf786dd0fd486822f8376c653ee8b567acae9e56563894f7eb02c9a2c728d49",
    }),
    "Linux": ("bin.linux26x86_64", {
        "p4": "ee6c23a899ff12f60e37b01dddbb9a12eafa051574b6f35978a098ff85d6f505",
        "p4d": "2444a4ca8ac634dc11d01e7e75d09787827bd6dc69239b408b03b2d6b83e2888",
    }),
}

FOSSIL_PINS = {
    "Windows": ("fossil-w64-2.28.zip", "1052b02b0594358d701170f8e2db7f948513cc44f44118a91369ab3f93641482"),
    "Linux": ("fossil-linux-x64-2.28.tar.gz", "cbd89e653e1b797802f2ee5bb55d6ad4959291ec6d6eb192c79ff62d9a224c33"),
}

def main():
    directory = Path(os.environ.get("RUNNER_TEMP", tempfile.gettempdir())) / "echo-p4-ci"
    directory.mkdir(parents=True, exist_ok=True)
    folder, binaries = PINS[platform.system()]
    for name, expected in binaries.items():
        with urllib.request.urlopen(f"https://cdist2.perforce.com/perforce/r24.1/{folder}/{name}", timeout=60) as response:
            data = response.read(64 << 20)
        actual = hashlib.sha256(data).hexdigest()
        if actual != expected:
            raise RuntimeError(f"{name}: expected {expected}, received {actual}; review a pin update before using a different binary")
        target = directory / name
        target.write_bytes(data)
        target.chmod(0o755)
        print(f"Verified {name}: {actual}")
    archive, expected = FOSSIL_PINS[platform.system()]
    with urllib.request.urlopen(f"https://fossil-scm.org/home/uv/{archive}?mimetype=application/octet-stream", timeout=60) as response:
        data = response.read(64 << 20)
    if hashlib.sha3_256(data).hexdigest() != expected:
        raise RuntimeError("Fossil archive differs from the published 2.28 SHA3-256 checksum")
    # Extract only the known executable; never trust archive paths for writes.
    if platform.system() == "Windows":
        name = "fossil.exe"
        with zipfile.ZipFile(io.BytesIO(data)) as package:
            binary = package.read(name)
    else:
        name = "fossil"
        with tarfile.open(fileobj=io.BytesIO(data), mode="r:gz") as package:
            member = next(item for item in package.getmembers() if item.isfile() and Path(item.name).name == name)
            binary = package.extractfile(member).read()
    target = directory / name
    target.write_bytes(binary)
    target.chmod(0o755)
    print(f"Verified Fossil 2.28: {expected}")
    if os.environ.get("GITHUB_PATH"):
        with open(os.environ["GITHUB_PATH"], "a", encoding="utf-8") as output:
            output.write(str(directory) + "\n")
    print(f"P4 fixture binaries: {directory}")

if __name__ == "__main__":
    main()
