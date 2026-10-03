# skyledger: one image, roles as subcommands (skyledger migrate|ingest|history|demo-feed).
FROM python:3.14-slim AS build
COPY --from=ghcr.io/astral-sh/uv:0.12.22 /uv /bin/uv
WORKDIR /app
ENV UV_COMPILE_BYTECODE=1 UV_LINK_MODE=copy UV_PYTHON_DOWNLOADS=never
# Dependencies first, so code changes don't reinstall them.
COPY pyproject.toml uv.lock ./
RUN uv sync --frozen --no-dev --no-install-project
COPY README.md LICENSE ./
COPY src ./src
RUN uv sync --frozen --no-dev --no-editable

FROM python:3.14-slim
RUN useradd --system --uid 10001 --no-create-home skyledger
COPY --from=build /app/.venv /app/.venv
ENV PATH=/app/.venv/bin:$PATH PYTHONUNBUFFERED=1
USER 10001
ENTRYPOINT ["skyledger"]
CMD ["--help"]
