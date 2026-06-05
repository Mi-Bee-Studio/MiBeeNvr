# Error Responses

All error responses follow this format:

```json
{
  "error": "Error message",
  "code": "ERROR_CODE"
}
```

## Error Code Reference

| Code | Description | HTTP Status |
|------|-------------|-------------|
| `CAMERA_NOT_FOUND` | Camera with specified ID does not exist | 404 |
| `CAMERA_ALREADY_RUNNING` | Camera recorder is already active | 400 |
| `CAMERA_DISABLED` | Camera is disabled and cannot be started | 400 |
| `CAMERA_ALREADY_EXISTS` | Camera with specified ID already exists | 409 |
| `RECORDING_NOT_FOUND` | Recording with specified ID does not exist | 404 |
| `STORAGE_FULL` | Disk space is critically low | 507 |
| `AUTH_REQUIRED` | Authentication is required | 401 |
| `AUTH_FAILED` | Authentication credentials were rejected | 401 |
| `INVALID_INPUT` | Request contains invalid parameters | 400 |
| `PATH_TRAVERSAL` | Path traversal attempt detected | 400 |
| `HLS_MAX_STREAMS` | Maximum concurrent HLS stream limit reached | 503 |
| `HLS_UNSUPPORTED_CODEC` | Camera codec is not supported for HLS streaming | 400 |
| `ONVIF_NOT_CAMERA` | Device is not an ONVIF camera | 400 |
| `ONVIF_CONNECTION_FAILED` | Failed to connect to ONVIF device | 500 |
| `ONVIF_NO_PROFILES` | No media profiles found for ONVIF camera | 400 |
| `INTERNAL` | Internal server error | 500 |

## Common Error Examples

### Authentication Failed
```json
{
  "error": "authentication failed: invalid username or password",
  "code": "AUTH_FAILED"
}
```

### Camera Not Found
```json
{
  "error": "camera not found: non-existent-camera",
  "code": "CAMERA_NOT_FOUND"
}
```

### Invalid Input
```json
{
  "error": "invalid input: camera URL must be valid",
  "code": "INVALID_INPUT"
}
```

### Storage Full
```json
{
  "error": "storage full: disk space critically low",
  "code": "STORAGE_FULL"
}
```

## HTTP Status Codes

| Status Code | Description |
|-------------|-------------|
| 200 | OK - Request successful |
| 201 | Created - Resource successfully created |
| 202 | Accepted - Request accepted for processing (e.g., async download) |
| 204 | No Content - Request successful, no response body |
| 400 | Bad Request - Invalid request parameters |
| 401 | Unauthorized - Authentication failed or required |
| 403 | Forbidden - Resource access not allowed |
| 404 | Not Found - Resource does not exist |
| 409 | Conflict - Resource conflict (e.g., duplicate camera ID) |
| 500 | Internal Server Error - Server-side error |
| 502 | Bad Gateway - Upstream service error (e.g., ONVIF probe) |
| 503 | Service Unavailable - Service temporarily unavailable |
| 507 | Insufficient Storage - Disk space insufficient |
