# ADR 005 — One checkpoint object store per project, per-session refs

Status: accepted (M2). Supersedes the per-session repositories of ADR 003 (a).

## Problem
M1.1 gave each session its own shadow repository so retention could follow the session. With
durable sessions (ADR 004), many sessions per project persist, and each full copy of the workspace
costs about half the tree's size (86 MB for a 157 MB tree). Twenty sessions would store the same
blobs twenty times.

## Verified git behaviour (experiment, git 2.56)
- `update-ref` accepts a **tree**, so a checkpoint ref needs no commit object.
- `gc` keeps objects reachable from refs and prunes the rest. Deleting a session's refs, then running
  gc, frees exactly its unique objects.
- `gc --prune=1.hour.ago` spares fresh unreferenced objects. With `--prune=now`, an object that only
  another process's private index referenced was deleted. That is the corruption to avoid.

## Decision
- One bare repo per project, `~/.cache/ternly/checkpoints/<project-key>.git`, with secret excludes
  and byte-exact attributes as before. One index file per session, `index-<session>`.
- **Refs per session** under `refs/ternly/<session>/`:
  - `head`: moved on every snapshot. It keeps the session index's objects reachable, so another
    process's gc can't prune them.
  - `cp-<tree>`: one per turn checkpoint and end-of-turn tree.
- **GC = objects unreachable from any saved session.**
  - Refs and index files of sessions that no longer exist in the session store are deleted, then
    `git gc --prune=1.hour.ago`. The grace period covers objects a live session wrote but hasn't
    referenced yet.
  - A `gc.lock` flock keeps two processes from collecting at once.
  - It runs in the background at startup and after `/delete`.
- **Disk cap:** `checkpoint_cap_mb` in config, default 2048, 0 = off.
  - Over the cap, the first startup warns, recording when.
  - A later startup still over the cap drops checkpoint refs of the least recently active sessions
    that aren't locked, oldest first, until under the cap. Conversations are kept.
  - `/rewind` on a pruned turn says the checkpoint was pruned to respect the cap.
- `/fork` copies refs (`update-ref`), not objects.
- The M1.1 per-session repos (`checkpoints/<key>/<session>.git`) are deleted on first start. They
  never outlived their process.

## Trade-offs
- One project's sessions share a repo, so a corrupted object store affects all of them. Mitigated by
  never pruning without the grace period.
- A ref-less write by a process that crashes leaves garbage until it is older than the grace period.
- Pruning by age is coarse. A user can keep an old session's checkpoints only by raising the cap.

A new session's index is seeded with a copy of the project's most recently used index, keeping its
mtime so git's racy-entry check still holds. git then reuses the cached file stats instead of
re-hashing the whole workspace.

## Measurement (Go source tree copy: 12,018 files, 157 MB)
| | shared store (M2) | per-session stores (M1.1) |
|---|---|---|
| disk, 20 sessions | **47.6 MB** | 951.5 MB |
| time to snapshot all 20 | **1.76 s** (9.1 s before index seeding) | 24.6 s |

Tests:
- Dropping a session and collecting frees its unique objects and keeps shared ones.
- A fork restores through copied refs after its parent is dropped.
- A foreign GC with `--prune=now` can't break a live session (its index is reachable through `head`).
- The default grace period spares fresh unreferenced objects.
- The cap warns first, then prunes oldest-first until under the cap, never touching the newest session.
