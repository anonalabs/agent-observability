from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Optional

import questionary
import typer
from rich.console import Console

from ..agents import AGENTS, detect_agents
from ..config import load_yaml_config, resolve

console = Console()

DEFAULT_ENDPOINT = "http://localhost:4317"


def _ask(result):
    """questionary returns None on Ctrl-C/Esc -- treat that as a clean abort."""
    if result is None:
        console.print("[yellow]Cancelled.[/yellow]")
        raise typer.Exit(1)
    return result


def _shell_rc_path() -> Path:
    shell = os.environ.get("SHELL", "")
    if "zsh" in shell:
        return Path.home() / ".zshrc"
    return Path.home() / ".bashrc"


def connect(
    agent: Optional[str] = typer.Option(None, "--agent", help="e.g. claude-code"),
    endpoint: Optional[str] = typer.Option(None, "--endpoint"),
    config: Optional[Path] = typer.Option(None, "--config", help="agentobs.yaml path"),
    write_shell_rc: Optional[bool] = typer.Option(
        None, "--write-shell-rc/--print-only", help=None
    ),
    log_user_prompts: Optional[bool] = typer.Option(
        None, "--log-user-prompts/--no-log-user-prompts",
        help="Capture full prompt text (privacy-sensitive, off by default)",
    ),
    log_tool_details: Optional[bool] = typer.Option(
        None, "--log-tool-details/--no-log-tool-details",
        help="Capture bash commands and file paths (privacy-sensitive, off by default)",
    ),
    non_interactive: bool = typer.Option(False, "--non-interactive", "--yes"),
) -> None:
    """Wire up an agent's telemetry env vars to point at the collector."""
    yaml_config = load_yaml_config(config)

    agent_name = resolve(
        "connect.agent",
        agent,
        yaml_config,
        prompt=lambda: _prompt_agent(),
        non_interactive=non_interactive,
    )
    if agent_name not in AGENTS:
        console.print(f"[red]Unknown agent '{agent_name}'. Supported: {', '.join(AGENTS)}[/red]")
        raise typer.Exit(1)

    resolved_endpoint = resolve(
        "connect.endpoint",
        endpoint,
        yaml_config,
        prompt=lambda: _ask(questionary.text("Collector OTLP endpoint:", default=DEFAULT_ENDPOINT).ask()),
        non_interactive=non_interactive,
        default=DEFAULT_ENDPOINT,
    )

    if agent_name == "cursor":
        _connect_cursor(resolved_endpoint, yaml_config, non_interactive=non_interactive)
        return

    if agent_name == "gemini-cli":
        _connect_gemini_cli(resolved_endpoint, yaml_config, non_interactive=non_interactive)
        return

    resolved_log_user_prompts = resolve(
        "connect.log_user_prompts",
        log_user_prompts,
        yaml_config,
        prompt=lambda: _ask(questionary.confirm(
            "Capture full user prompt text? (privacy-sensitive)", default=False
        ).ask()),
        non_interactive=non_interactive,
        default=False,
    )
    resolved_log_tool_details = resolve(
        "connect.log_tool_details",
        log_tool_details,
        yaml_config,
        prompt=lambda: _ask(questionary.confirm(
            "Capture tool details (bash commands, file paths)? (privacy-sensitive)", default=False
        ).ask()),
        non_interactive=non_interactive,
        default=False,
    )

    env_vars = AGENTS[agent_name].env_vars(
        resolved_endpoint,
        log_user_prompts=resolved_log_user_prompts,
        log_tool_details=resolved_log_tool_details,
    )
    export_lines = [f'export {key}="{value}"' for key, value in env_vars.items()]

    should_write = resolve(
        "connect.write_shell_rc",
        write_shell_rc,
        yaml_config,
        prompt=lambda: _ask(questionary.confirm(
            f"Append these to {_shell_rc_path()}?", default=False
        ).ask()),
        non_interactive=non_interactive,
        default=False,
    )

    if should_write:
        rc_path = _shell_rc_path()
        with rc_path.open("a") as f:
            f.write("\n# added by `agentobs connect`\n")
            f.write("\n".join(export_lines) + "\n")
        console.print(f"[green]Wrote env vars to {rc_path}. Restart your shell or `source {rc_path}`.[/green]")
    else:
        console.print("Copy these into your shell:\n")
        for line in export_lines:
            console.print(line)


def _connect_cursor(endpoint: str, yaml_config: dict, *, non_interactive: bool) -> None:
    agent = AGENTS["cursor"]

    if shutil.which("cursor-otel-hook") is None:
        package_path = agent.vendored_package_path()
        should_install = non_interactive or _ask(questionary.confirm(
            f"`cursor-otel-hook` isn't installed. Install it now from {package_path}?", default=True
        ).ask())
        if should_install:
            result = subprocess.run([sys.executable, "-m", "pip", "install", str(package_path)])
            if result.returncode != 0:
                console.print("[red]Install failed -- see pip output above.[/red]")
                raise typer.Exit(1)
            if shutil.which("cursor-otel-hook") is None:
                console.print(
                    "[yellow]Installed, but `cursor-otel-hook` still isn't on PATH "
                    "(check your Python scripts dir is on PATH).[/yellow]"
                )
        else:
            console.print("[yellow]Skipping install -- hooks won't work until it's installed.[/yellow]")

    mask_prompts = resolve(
        "connect.mask_prompts",
        None,
        yaml_config,
        prompt=lambda: _ask(questionary.confirm(
            "Mask prompts/file paths/emails? (privacy)", default=False
        ).ask()),
        non_interactive=non_interactive,
        default=False,
    )

    hooks_dir = agent.hooks_dir()
    hooks_dir.mkdir(parents=True, exist_ok=True)

    config_path = agent.config_path()
    _backup_if_exists(config_path)
    config_path.write_text(json.dumps(agent.otel_config(endpoint, mask_prompts=mask_prompts), indent=2))

    wrapper_path = agent.wrapper_script_path()
    _backup_if_exists(wrapper_path)
    wrapper_path.write_text(agent.wrapper_script())
    wrapper_path.chmod(0o755)

    hooks_json_path = agent.hooks_json_path()
    existing_hooks = None
    if hooks_json_path.exists():
        _backup_if_exists(hooks_json_path)
        existing_hooks = json.loads(hooks_json_path.read_text())
    hooks_json_path.write_text(json.dumps(agent.hooks_json(existing=existing_hooks), indent=2))

    console.print(f"[green]Wrote {config_path}, {wrapper_path}, and {hooks_json_path} (merged, not replaced).[/green]")
    console.print("Restart Cursor IDE to pick up the new hooks.")


def _connect_gemini_cli(endpoint: str, yaml_config: dict, *, non_interactive: bool) -> None:
    agent = AGENTS["gemini-cli"]

    log_prompts = resolve(
        "connect.log_prompts",
        None,
        yaml_config,
        prompt=lambda: _ask(questionary.confirm(
            "Log full prompt text? (privacy-sensitive)", default=False
        ).ask()),
        non_interactive=non_interactive,
        default=False,
    )

    settings_path = agent.settings_path()
    settings_path.parent.mkdir(parents=True, exist_ok=True)
    existing = None
    if settings_path.exists():
        _backup_if_exists(settings_path)
        existing = json.loads(settings_path.read_text())
    merged = agent.merge_settings(existing, log_prompts=log_prompts)
    settings_path.write_text(json.dumps(merged, indent=2))

    console.print(f"[green]Wrote {settings_path} (merged, not replaced).[/green]")
    console.print(f'Also export: export OTEL_EXPORTER_OTLP_ENDPOINT="{endpoint}"')
    console.print("(only needed if the collector isn't at the settings.json default of localhost:4317)")


def _backup_if_exists(path: Path) -> None:
    if path.exists():
        backup = path.with_suffix(path.suffix + ".bak")
        backup.write_bytes(path.read_bytes())


def _prompt_agent() -> str:
    detected = detect_agents()
    default = detected[0].name if detected else "claude-code"
    if detected:
        console.print(f"Detected agent: {default}")
    return _ask(questionary.select(
        "Agent to configure:", choices=list(AGENTS), default=default
    ).ask())
