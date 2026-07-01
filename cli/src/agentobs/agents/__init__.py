from .claude_code import ClaudeCodeAgent
from .cursor import CursorAgent
from .gemini_cli import GeminiCliAgent

AGENTS = {
    "claude-code": ClaudeCodeAgent(),
    "cursor": CursorAgent(),
    "gemini-cli": GeminiCliAgent(),
}


def detect_agents() -> list:
    return [agent for agent in AGENTS.values() if agent.detect()]
