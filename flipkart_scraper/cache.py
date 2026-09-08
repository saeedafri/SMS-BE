"""Small JSON cache used to resume recent scraper runs."""

from __future__ import annotations

import json
import time
from pathlib import Path
from typing import Callable


class RowCache:
    """Store parsed rows briefly so retries do not download them again."""

    def __init__(
        self,
        path: str | Path,
        max_age_hours: float = 1,
        now: Callable[[], float] = time.time,
    ) -> None:
        self.path = Path(path)
        self.max_age_seconds = max(0, max_age_hours) * 3600
        self.now = now
        self.entries = self._load()

    def _load(self) -> dict:
        if not self.path.exists():
            return {}
        try:
            data = json.loads(self.path.read_text(encoding="utf-8"))
            return data if isinstance(data, dict) else {}
        except (OSError, json.JSONDecodeError):
            return {}

    def get(self, key: str) -> dict | None:
        entry = self.entries.get(key)
        if not isinstance(entry, dict) or self.max_age_seconds == 0:
            return None
        saved_at = entry.get("saved_at")
        row = entry.get("row")
        if not isinstance(saved_at, (int, float)) or not isinstance(row, dict):
            return None
        if self.now() - saved_at > self.max_age_seconds:
            return None
        return row.copy()

    def put(self, key: str, row: dict) -> None:
        self.entries[key] = {"saved_at": self.now(), "row": row}

    def save(self) -> None:
        self.path.parent.mkdir(parents=True, exist_ok=True)
        temporary = self.path.with_suffix(f"{self.path.suffix}.tmp")
        temporary.write_text(json.dumps(self.entries, ensure_ascii=False), encoding="utf-8")
        temporary.replace(self.path)
