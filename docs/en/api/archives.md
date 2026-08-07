# Archive API

## List Archives

**Endpoint:** `GET /api/archives`

List all archive groups by camera.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/archives"
```

**Response:**
```json
{
  "archives": [
    {
      "camera_id": "front-door",
      "retention_days": 30,
      "recordings_count": 150,
      "total_size_mb": 1024
    }
  ]
}
```

## List Archive Recordings

**Endpoint:** `GET /api/archives/{id}/recordings`

List recordings for a specific archive.

**Request:**
```bash
curl -u username:password \
  "http://localhost:9090/api/archives/front-door/recordings"
```

**Response:**
```json
{
  "recordings": [
    {
      "id": "1704123456789012345",
      "started_at": "2024-01-01T12:34:56.789Z",
      "duration": 10.0,
      "file_size": 1048576
    }
  ],
  "total": 1
}
```

## Delete Archive Group

**Endpoint:** `DELETE /api/archives/{id}`

Delete an entire archive group for a camera.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/archives/front-door"
```

**Response:**
```json
{
  "status": "deleted"
}
```

## Delete Archive Recording

**Endpoint:** `DELETE /api/archives/{id}/recordings/{recordingID}`

Delete a specific recording from an archive.

**Request:**
```bash
curl -u username:password \
  -X DELETE \
  "http://localhost:9090/api/archives/front-door/recordings/1704123456789012345"
```

**Response:**
```json
{
  "status": "deleted"
}
```

## Set Archive Retention

**Endpoint:** `PUT /api/archives/{id}/retention`

Set retention period for an archive group.

**Request Body:**
```json
{
  "retention_days": 60
}
```

**Request:**
```bash
curl -u username:password \
  -X PUT \
  -H "Content-Type: application/json" \
  -d '{
    "retention_days": 60
  }' \
  "http://localhost:9090/api/archives/front-door/retention"
```

**Response:**
```json
{
  "status": "updated"
}
```
