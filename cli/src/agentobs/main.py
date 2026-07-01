import typer

from .commands.connect import connect
from .commands.export import export
from .commands.install import install
from .commands.status import status

app = typer.Typer(help="Install, connect, and export telemetry for AI coding agents.")

app.command()(install)
app.command()(connect)
app.command()(export)
app.command()(status)


if __name__ == "__main__":
    app()
