import pytest

from agentobs.config import MissingConfigError, resolve


def test_flag_wins_over_yaml_and_prompt():
    called = {"prompt": False}

    def prompt():
        called["prompt"] = True
        return "prompt-value"

    result = resolve(
        "connect.endpoint", "flag-value", {"connect": {"endpoint": "yaml-value"}}, prompt
    )
    assert result == "flag-value"
    assert called["prompt"] is False


def test_yaml_wins_over_prompt():
    called = {"prompt": False}

    def prompt():
        called["prompt"] = True
        return "prompt-value"

    result = resolve("connect.endpoint", None, {"connect": {"endpoint": "yaml-value"}}, prompt)
    assert result == "yaml-value"
    assert called["prompt"] is False


def test_prompt_used_when_nothing_else_set():
    result = resolve("connect.endpoint", None, {}, lambda: "prompt-value")
    assert result == "prompt-value"


def test_non_interactive_uses_default_without_prompting():
    called = {"prompt": False}

    def prompt():
        called["prompt"] = True
        return "prompt-value"

    result = resolve(
        "connect.endpoint", None, {}, prompt, non_interactive=True, default="default-value"
    )
    assert result == "default-value"
    assert called["prompt"] is False


def test_non_interactive_raises_when_no_default():
    with pytest.raises(MissingConfigError):
        resolve("connect.endpoint", None, {}, lambda: "prompt-value", non_interactive=True)
