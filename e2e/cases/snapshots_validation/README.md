# snapshots_validation

## Setup

The case starts a real daemon with persistent file storage and an otherwise
empty graph. It uses only userspace resources.

## Actions and Expected Behavior

- Snapshot operations reject malformed UUIDs as invalid input.
- Export, delete, rename, and restore report a missing valid UUID as not found.
- Import rejects an empty file and data that is not a ZIP archive.
- Snapshot creation and rename reject names above the 256-byte limit.
- Import rejects an archive whose UUID already exists, without changing the
  stored snapshot.
- Rejected operations leave the daemon responsive and the valid snapshot usable.
