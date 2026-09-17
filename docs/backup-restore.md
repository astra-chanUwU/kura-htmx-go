# Offline backup and restore

Kura's maintenance command creates a portable ZIP containing a consistent SQLite snapshot, the originals and thumbnails referenced by every post (including drafts, quarantined posts, and soft-deleted posts), and a checksum manifest. It does not upload data or provide cloud/off-site automation.

## Important: stop Kura first

Backups are offline operations. Stop the Kura process and make sure no other process is writing the database or media root before running `backup`. The command refuses pending `.deleting` or `.incoming` staging; reconcile or resolve that state before continuing.

## Backup to an external location

Choose an external disk or other trusted local destination with enough free space. The archive must not be inside Kura's media root, and an existing archive is never overwritten.

```sh
cd /path/to/kura
go run ./cmd/kura-maintenance backup \
  -db ./var/kura.db \
  -media ./var/media \
  -out "/Volumes/Backup/Kura/kura-backup-$(date +%Y%m%d-%H%M%S).zip"
```

The database snapshot uses SQLite's native backup API, so committed data in a WAL is included. The archive contains no absolute source paths.

## Verify a backup

Verification is read-only with respect to the archive and the destination filesystem. It checks the manifest version and structure, duplicate and unsafe entries, sizes, SHA-256 checksums, SQLite integrity/foreign keys and migrations, and that every database-referenced original and thumbnail is present.

```sh
go run ./cmd/kura-maintenance verify \
  -in "/Volumes/Backup/Kura/kura-backup-YYYYMMDD-HHMMSS.zip"
```

## Check a live archive offline

Stop Kura before checking its database and media root. The `check` command is
strictly read-only: it does not run migrations, repair metadata, or remove,
rename, or rewrite database/media files. It checks SQLite integrity and foreign
keys, confirms that every shipped migration is recorded, verifies each post's
contained original (size and SHA-256) and decodable thumbnail, and reports
orphan files beneath `originals/` and `thumbs/`. `.incoming` and `.deleting`
are reported separately as pending staging and are never cleaned up by this
command.

```sh
cd /path/to/kura
go run ./cmd/kura-maintenance check \
  -db ./var/kura.db \
  -media ./var/media
```

A clean archive prints a short report and exits `0`:

```
CHECK PASS
posts: 48
referenced media: 96
orphan media: 0
```

Any integrity, path, media, orphan, symlink, or staging finding prints
`CHECK FAIL` with deterministic `finding:` lines and exits nonzero. Keep the
output for the operator; this command only diagnoses the archive. Resolve
findings using the normal Kura workflow, then stop Kura and run the check again.

## Restore into new paths

Restore only into new destination paths. Do not point it at the live `var/kura.db`, the live `var/media`, an existing workspace, or a directory containing unrelated files. The command fully validates the archive before staging and atomically renames the staged database and media tree into place; it never overwrites or deletes an existing destination.

```sh
cd /path/to/kura
restore_root="/Volumes/SSD/Kura-restored-$(date +%Y%m%d-%H%M%S)"
mkdir -p "$restore_root"
go run ./cmd/kura-maintenance restore \
  -in "/Volumes/Backup/Kura/kura-backup-YYYYMMDD-HHMMSS.zip" \
  -db "$restore_root/kura.db" \
  -media "$restore_root/media"
```

Restoration preserves users, roles, stored sessions, pools, favorites, tags, uploader attribution, quarantine/audit records, originals, and thumbnails. Restoring active sessions is intentional. If the backup leaves trusted custody, revoke sessions from the restored instance before treating it as private again.

Start the restored instance explicitly with its new paths:

```sh
KURA_RP_ID=localhost KURA_ORIGINS=http://localhost:8080 \
  go run ./cmd/kura \
  -db "$restore_root/kura.db" \
  -media "$restore_root/media" \
  -addr 127.0.0.1:8080
```

## Rollback

The backup and restore commands do not change the live paths. To roll back after a deliberate cutover, stop Kura and start it again with the old `-db` and `-media` paths:

```sh
go run ./cmd/kura \
  -db ./var/kura.db \
  -media ./var/media \
  -addr 127.0.0.1:8080
```

Keep the restored paths until the rollback decision is complete. Do not delete the old paths as part of this drill.
