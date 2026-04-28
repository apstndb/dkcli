# dkcli Gemini review guidelines

Focus review comments on correctness, security, and meaningful maintainability issues.
Avoid low-value repetition when the code already documents an intentional tradeoff or
platform constraint clearly.

## Toolchain and modern Go

- Treat `go.mod` and the declared toolchain as the source of truth for available Go
  language features and standard-library APIs.
- This repository targets modern Go. Prefer current Go idioms and APIs that are
  available in the repo toolchain.
- Do not infer the repository's minimum supported Go version from build tags,
  comments, or compatibility notes.
- Do not suggest backports or older-Go compatibility workarounds unless the change
  explicitly targets older Go versions.

## High-signal review expectations

- Prioritize correctness and security over speculative style nits.
- Treat comments that only restate an already documented rationale as low value
  unless the code is still incorrect or unsafe.
- For platform-specific code, prefer comments that identify a real safety issue or
  a missing user-visible warning. If a platform limitation is unavoidable and the
  code explains it clearly, avoid repeating the same concern.

## Repository-specific conventions

- Use `AGENTS.md` as the source of truth for repository-specific behavior and
  command conventions.
- For user-facing CLI behavior, keep an eye on consistency between implementation,
  tests, and the docs in `README.md` and `SKILL.md`.
- Structured YAML output in this repository uses `snake_case` field names.
- Status or summary output on stderr is often intentional; payload output belongs
  on stdout or the selected output file.
