"""Constants for the MiBee NVR integration."""

DOMAIN = "mibee_nvr"

CONF_HOST = "host"
CONF_PORT = "port"
CONF_USERNAME = "username"
CONF_PASSWORD = "password"
CONF_VERIFY_SSL = "verify_ssl"
CONF_RTSP_PORT = "rtsp_port"
CONF_SCAN_INTERVAL = "scan_interval"

DEFAULT_PORT = 9090
DEFAULT_RTSP_PORT = 8554
DEFAULT_VERIFY_SSL = False
DEFAULT_SCAN_INTERVAL = 30
MIN_SCAN_INTERVAL = 5

# Camera status vocabulary from GET /api/cameras / GET /api/health.
# "stopped" means the NVR is deliberately not pulling the camera
# (recording disabled) — it says nothing about connectivity, so the
# connected sensor reports unavailable for it rather than a guess.
CONNECTED_STATUSES = {"recording", "idle", "running", "online"}
DISCONNECTED_STATUSES = {"reconnecting", "error", "offline", "unreachable"}
RECORDING_STATUS = "recording"

# encoding values the camera list reports; JPEG-family cameras are served
# by the NVR's MJPEG passthrough, H.264/H.265 by the RTSP output server.
JPEG_ENCODINGS = {"mjpeg", "jpeg", "mjpa"}
STREAM_ENCODINGS = {"h264", "h265", "hevc"}

ATTR_CAMERA_ID = "camera_id"
ATTR_ENCODING = "encoding"
ATTR_PROTOCOL = "protocol"
ATTR_STATUS = "status"
