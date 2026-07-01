"""Config precedence: CLI flag > YAML file > interactive prompt > documented default."""

from __future__ import annotations

from pathlib import Path
from typing import Any, Callable, Optional

import yaml


class MissingConfigError(Exception):
    def __init__(self, key: str):
        super().__init__(
            f"missing required value for '{key}' and --non-interactive was set "
            "(pass it as a flag or in --config yaml)"
        )
        self.key = key


def load_yaml_config(path: Optional[Path]) -> dict:
    if path is None:
        return {}
    if not path.exists():
        raise FileNotFoundError(f"config file not found: {path}")
    with path.open() as f:
        data = yaml.safe_load(f) or {}
    if not isinstance(data, dict):
        raise ValueError(f"config file {path} must contain a mapping at the top level")
    return data


def get_nested(data: dict, dotted_key: str) -> Any:
    node = data
    for part in dotted_key.split("."):
        if not isinstance(node, dict) or part not in node:
            return None
        node = node[part]
    return node


def resolve(
    dotted_key: str,
    flag_value: Any,
    yaml_config: dict,
    prompt: Callable[[], Any],
    *,
    non_interactive: bool = False,
    default: Any = None,
) -> Any:
    """Resolve a single config value under the documented precedence order."""
    if flag_value is not None:
        return flag_value

    yaml_value = get_nested(yaml_config, dotted_key)
    if yaml_value is not None:
        return yaml_value

    if non_interactive:
        if default is not None:
            return default
        raise MissingConfigError(dotted_key)

    return prompt()
