# AGENTS.md

## Goal

Build a production-quality Garmin workout viewer with Go backend, Angular frontend and PostgreSQL. Prioritize a functional MVP, simple architecture, correctness and low token usage.

## Stack

- Go: latest stable release.
- Angular: latest stable release, strict TypeScript, standalone components.
- PostgreSQL: supported stable release.
- REST API documented with OpenAPI.
- Docker Compose for local dependencies.
- Stable, maintained dependencies only. Verify compatibility before choosing versions; commit lockfiles.

## Product

- Dashboard: activity count, distance, duration, weekly/monthly trends and sport distribution.
- Activity list: pagination, sorting, date/sport/text filters.
- Activity detail: available metrics, time-series charts, laps and GPS map.
- Import local FIT, TCX and GPX files.
- Show import errors and results; handle duplicates idempotently where possible.
- Normalize sports, units and timestamps.
- Never invent missing metrics or represent missing values as zero.
- Keep Garmin Connect integration optional. Verify official access before implementing it; no invented endpoints, scraping or stored Garmin passwords.

## Architecture

Monorepo:

- `backend/`: Go API, domain, application logic, persistence, imports and migrations.
- `frontend/`: Angular features, core services and shared UI.
- `docs/`: architecture and OpenAPI.
- Root: `AGENTS.md`, README, Makefile, Compose and `.env.example`.

Adapt to existing repository conventions. Do not create empty abstractions or unnecessary layers.

### Backend

- Prefer standard `net/http` and `http.ServeMux`; add dependencies only with justification.
- Separate domain logic from HTTP, persistence and file parsing.
- Use explicit dependency injection and small interfaces where useful.
- Use contexts, timeouts, graceful shutdown, structured logs and wrapped errors.
- PostgreSQL migrations, parameterized queries, transactions, integrity constraints and appropriate indexes.
- Store activities separately from time-series samples and laps.
- Use UTC timestamps; preserve timezone information needed for display.
- Nullable fields for unavailable metrics.
- Version API under `/api/v1`; document schemas, validation, pagination and errors in OpenAPI.
- Suggested endpoints: activities list/detail, samples, laps, imports, summary statistics, `/healthz`, `/readyz`.

### Frontend

- Angular standalone components, strict TypeScript, Router, `HttpClient`, Signals and RxJS where appropriate.
- Reactive forms for complex forms.
- Feature-based organization: dashboard, activities, activity detail, imports.
- Typed API clients; avoid `any`, duplicated business logic and unnecessary global state.
- Responsive, accessible UI with loading, empty, error and success states.
- Charts and maps only when real data exists. Use maintained compatible libraries.
- Localized dates, units and durations.
- Avoid expensive template computations, unmanaged subscriptions and unsafe DOM bypasses.

### Data import and security

- Parse files in the backend through format-specific adapters.
- Validate actual file content, size, format, timestamps and units.
- Treat files as untrusted; protect against malformed input, path traversal, resource exhaustion and oversized uploads.
- Normalize into a shared domain model.
- Persist activity and samples atomically.
- Detect duplicates using source IDs or content fingerprints where appropriate.
- Document unsupported fields and parser limitations.
- Configure explicit CORS origins; use parameterized SQL and safe errors.
- Keep secrets out of Git. Do not expose database ports unnecessarily.
- If multiple users or public deployment are introduced, implement authentication, authorization and data isolation.

## Quality

Backend:

- `gofmt`, `go vet ./...`, `go test ./...`.
- Unit tests for domain logic, parsing, normalization and error cases.
- Integration tests for PostgreSQL, transactions and HTTP endpoints.

Frontend:

- Unit tests for services, components, filters and UI states.
- Production build and available type/lint checks.
- End-to-end tests for critical flows when practical.

Test missing metrics, invalid files, duplicates, pagination, import failures and long sessions. Never claim checks passed unless actually executed.

## Workflow

1. Inspect repository, Git status and applicable instructions.
2. Verify toolchain versions and existing conventions.
3. Implement small, coherent increments.
4. Add/update tests and documentation alongside changes.
5. Run relevant checks; review the full diff.
6. Report changes, commands/results and remaining limitations concisely.

Preserve existing user work. Avoid unrelated rewrites, speculative features, unnecessary dependencies and destructive changes. Ask before irreversible operations.

## Caveman and RTK

- Use Caveman if installed and compatible with Pi.
- Use RTK only if the correct binary and Pi integration are available.
- Keep responses, command output summaries and progress reports concise.
- Preserve source code, error details, security warnings and test results accurately.
- Do not confuse different executables named `rtk`; verify `command -v rtk`, `rtk --version` and required subcommands.
- Never sacrifice code quality, testing or correctness for token savings.

## MVP acceptance

- Local setup documented and reproducible.
- PostgreSQL persistence survives restarts.
- Supported FIT/TCX/GPX imports work and report failures clearly.
- Activity list, filters, pagination, dashboard and detail views use persisted data.
- Charts and maps display only available real data.
- API contract documented; migrations and tests pass.
- Responsive accessible UI and production build.
- Core functionality works without Garmin Connect credentials.

## Implementation order

1. Inspect repo; scaffold only what is missing.
2. Set up Go, Angular, PostgreSQL, migrations and OpenAPI.
3. Implement domain, persistence and query API.
4. Implement and test file importers.
5. Build dashboard, list, detail, charts, maps and import UI.
6. Complete security, tests, docs and production build.

Keep every phase buildable. Start with the smallest working vertical slice, then expand.

don't read .env file
