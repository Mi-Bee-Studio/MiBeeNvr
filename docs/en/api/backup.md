# Backup API

## Create Backup

**Endpoint:** `POST /api/backup`

Create a backup of the database.

**Request:**
```bash
curl -u username:password \
  -X POST \
  "http://localhost:9090/api/backup"
```

**Response:**
```json
{
  "status": "created",
  "file": "nvr-backup-20240101-123456.db"
}
```

## List Backups

**Endpoint:** `GET /api/backups`

List available backup files.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/backups"
```

**Response:**
```json
["nvr-backup-20240101-123456.db", "nvr-backup-20240102-091234.db"]
```
