# Copyright 2026 The CALM Authors
# SPDX-License-Identifier: Apache-2.0

from __future__ import annotations

import json
from dataclasses import dataclass
from pathlib import Path
from typing import Any


class UsageError(Exception):
    """Raised for CLI configuration problems that argparse cannot express."""


@dataclass(frozen=True)
class FixtureDoc:
    source: str
    content: str
    format_hint: str
    content_type: str

    @property
    def raw_bytes(self) -> int:
        return len(self.content.encode("utf-8"))


@dataclass(frozen=True)
class GoldenQuery:
    query: str
    search_queries: tuple[str, ...]
    expected_sources: tuple[str, ...]
    evidence_terms: tuple[str, ...]
    required: bool = True


def load_fixtures(root: Path) -> list[FixtureDoc]:
    if not root.exists():
        raise UsageError(f"fixture directory does not exist: {root}")
    docs: list[FixtureDoc] = []
    for path in sorted(root.rglob("*")):
        if not path.is_file() or path.name.startswith("."):
            continue
        content = strip_repository_license(path.read_text(encoding="utf-8"))
        docs.append(
            FixtureDoc(
                source=path.relative_to(root).as_posix(),
                content=content,
                format_hint=format_hint(path),
                content_type=content_type(path),
            ),
        )
    if not docs:
        raise UsageError(f"no fixture files found under {root}")
    return docs


def load_golden_queries(path: Path) -> list[GoldenQuery]:
    raw = json.loads(path.read_text(encoding="utf-8"))
    if not isinstance(raw, list):
        raise UsageError(f"{path}: expected a JSON array")
    return [golden_query_from_object(path, index, item) for index, item in enumerate(raw, start=1)]


def golden_query_from_object(path: Path, index: int, value: Any) -> GoldenQuery:
    if not isinstance(value, dict):
        raise UsageError(f"{path}: query #{index} must be an object")
    query = value.get("query")
    search_queries = value.get("search_queries", [query])
    expected_sources = value.get("expected_sources", [])
    evidence_terms = value.get("evidence_terms", [])
    required = value.get("required", True)
    if not isinstance(query, str) or not query:
        raise UsageError(f"{path}: query #{index} missing non-empty query")
    if not isinstance(search_queries, list) or not all(isinstance(item, str) and item for item in search_queries):
        raise UsageError(f"{path}: query #{index} search_queries must be a non-empty string array")
    if len(search_queries) > 10:
        raise UsageError(f"{path}: query #{index} search_queries must contain at most 10 items")
    if not isinstance(expected_sources, list) or not all(isinstance(item, str) for item in expected_sources):
        raise UsageError(f"{path}: query #{index} expected_sources must be a string array")
    if not isinstance(evidence_terms, list) or not all(isinstance(item, str) for item in evidence_terms):
        raise UsageError(f"{path}: query #{index} evidence_terms must be a string array")
    if not isinstance(required, bool):
        raise UsageError(f"{path}: query #{index} required must be boolean")
    return GoldenQuery(
        query=query,
        search_queries=tuple(search_queries),
        expected_sources=tuple(expected_sources),
        evidence_terms=tuple(evidence_terms),
        required=required,
    )


def strip_repository_license(content: str) -> str:
    stripped = content.lstrip()
    leading_ws = content[: len(content) - len(stripped)]
    if not stripped.startswith("<!--"):
        return content
    end = stripped.find("-->")
    if end == -1:
        return content
    comment = stripped[: end + 3]
    if "SPDX-License-Identifier" not in comment:
        return content
    rest = stripped[end + 3 :]
    return leading_ws + rest.lstrip("\n")


def format_hint(path: Path) -> str:
    suffix = path.suffix.lower()
    if suffix == ".md":
        return "markdown"
    if suffix == ".json":
        return "json"
    if suffix in {".jsonl", ".log"}:
        return "log"
    if suffix == ".csv":
        return "csv"
    if suffix == ".tsv":
        return "tsv"
    return "text"


def content_type(path: Path) -> str:
    if path.suffix.lower() in {".json", ".jsonl", ".log"}:
        return "code"
    return "prose"
