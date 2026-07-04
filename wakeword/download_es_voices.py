"""Download all Spanish Piper voices into the trainer's voices dir.

Replicates the trainer UI's _catalog_voice_files("es") + _download_to_path so the
direct training script finds es_*.onnx. Max speaker diversity = lower false-reject.
"""

import json
import sys
import urllib.request
from pathlib import Path

CATALOG = "https://huggingface.co/rhasspy/piper-voices/raw/main/voices.json"
ROOT = "https://huggingface.co/rhasspy/piper-voices/resolve/main"
DEST = Path("trainer/piper-sample-generator/voices")


def fetch(url: str) -> bytes:
    req = urllib.request.Request(url, headers={"User-Agent": "mww-trainer/1.0"})
    with urllib.request.urlopen(req, timeout=120) as r:
        return r.read()


def main() -> None:
    DEST.mkdir(parents=True, exist_ok=True)
    catalog = json.loads(fetch(CATALOG))
    files: dict[str, str] = {}
    for entry in catalog.values():
        if not isinstance(entry, dict):
            continue
        for rel in (entry.get("files") or {}):
            if rel.startswith("es/") and (rel.endswith(".onnx") or rel.endswith(".onnx.json")):
                files[Path(rel).name] = f"{ROOT}/{rel}"

    onnx = sorted(n for n in files if n.endswith(".onnx"))
    print(f"Spanish voices found: {len(onnx)} ({len(files)} files total)")
    for name in onnx:
        print("  -", name)

    for i, (name, url) in enumerate(sorted(files.items()), 1):
        dest = DEST / name
        if dest.exists() and dest.stat().st_size > 0:
            print(f"[{i}/{len(files)}] skip (exists) {name}")
            continue
        print(f"[{i}/{len(files)}] downloading {name} ...", flush=True)
        tmp = dest.with_suffix(dest.suffix + ".tmp")
        with urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": "mww-trainer/1.0"}), timeout=300) as r, open(tmp, "wb") as out:
            while chunk := r.read(1 << 20):
                out.write(chunk)
        tmp.replace(dest)

    print(f"DONE. {len(onnx)} Spanish voices in {DEST}")


if __name__ == "__main__":
    sys.exit(main())
