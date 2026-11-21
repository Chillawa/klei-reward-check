import argparse
import json
from pathlib import Path

from check_sources import SOURCES, atomic_write, check_sources


def healthy_sources(sources):
    return isinstance(sources, dict) and sources.keys() == SOURCES.keys() and all(
        isinstance(source, dict) and source.get("status") in ("ok", "limited_history")
        and source.get("errors") == []
        for source in sources.values()
    )


def public_listing(report):
    return {"version": 1, "checked_at": report["scanned_at_utc"],
            "sources": sorted(report["sources"]), "complete_scan": healthy_sources(report["sources"]),
            "rewards": [{key: entry[key] for key in ("code", "target", "evidence")}
                        for entry in report["candidates"]]}


def main():
    parser = argparse.ArgumentParser(description="生成只含公开公告和奖励链接的清单。")
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    listing = public_listing(check_sources())
    atomic_write(args.output, json.dumps(listing, ensure_ascii=False, indent=2).encode())


if __name__ == "__main__":
    main()
