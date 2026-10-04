# Backlog

Known gaps, each found while building or measuring a milestone. They are scheduled before the
milestone named, or noted as open.

## Code graph (before M7: multi-language graph)
- **Generics in `implementations`.** Method sets of generic types and interfaces are skipped, so
  `type Set[T comparable]` never matches an interface. It needs instantiation-aware matching, or at
  least matching on the generic method shapes. (ADR 007)
- **Calls through function values.** `f := pkg.Func; f()`, method values, and callbacks passed as
  arguments are not call edges. Static assignment tracking within a function would cover the common
  cases. (ADR 007)
- **Approximate first pass** (M3.1): name-based references miss uses through aliases and dot
  imports, and shadowing can produce false references. It is replaced by the typed graph as soon as
  that build finishes, so this only matters during the first minutes on a cold Go cache.
- **Type-error drift after incremental API changes** is now bounded by the idle rebuild (M3.1).
  While a session stays busy, the inexact graph can persist.

## Models
- **Retired or removed models** (seen 2026-10-04: `HTTP 410: glm-5.1 was retired` from Ollama
  Cloud): drop the model from routing for the session on 404/410 and fail over, instead of ending
  the turn with an error.

## UI (M5)
- **bubbletea v2 migration** (ADR 006). It removes the 5 s start-up stall in terminals that ignore
  status queries, and enables keyboard enhancements.

## Infrastructure
- **CI** is blocked by a GitHub account billing lock (check-run annotation: "The job was not started
  because your account is locked due to a billing issue"). The workflows are ready; they need the
  account unlocked.
