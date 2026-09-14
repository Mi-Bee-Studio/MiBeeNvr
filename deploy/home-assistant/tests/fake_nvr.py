"""A fake MiBee NVR API server for tests (aiohttp, no homeassistant imports)."""

from __future__ import annotations

from aiohttp import web

USERNAME = "admin"
PASSWORD = "hunter2"

HEALTH = {
    "status": "ok",
    "version": "v0.12.1-test",
    "device_id": "nvr-test-device",
}

CAMERAS = [
    {
        "id": "front-door",
        "name": "Front Door",
        "status": "recording",
        "protocol": "rtsp",
        "encoding": "h264",
        "recording_enabled": True,
    },
    {
        "id": "yard-esp32",
        "name": "Yard ESP32",
        "status": "reconnecting",
        "protocol": "http",
        "encoding": "mjpeg",
        "recording_enabled": True,
    },
    {
        "id": "garage",
        "name": "Garage",
        "status": "stopped",
        "protocol": "onvif",
        "encoding": "",
        "recording_enabled": False,
    },
]


def _authorized(request: web.Request) -> bool:
    auth = request.headers.get("Authorization", "")
    import base64

    expected = base64.b64encode(f"{USERNAME}:{PASSWORD}".encode()).decode()
    return auth == f"Basic {expected}"


def build_app() -> web.Application:
    app = web.Application()

    async def health(_request: web.Request) -> web.Response:
        return web.json_response(HEALTH)

    async def cameras(request: web.Request) -> web.Response:
        if not _authorized(request):
            return web.json_response({"error": "unauthorized"}, status=401)
        return web.json_response(CAMERAS)

    async def get_camera(request: web.Request) -> web.Response:
        if not _authorized(request):
            return web.json_response({"error": "unauthorized"}, status=401)
        cam_id = request.match_info["camera_id"]
        for cam in CAMERAS:
            if cam["id"] == cam_id:
                return web.json_response(cam)
        return web.json_response({"error": "not found"}, status=404)

    async def update_camera(request: web.Request) -> web.Response:
        if not _authorized(request):
            return web.json_response({"error": "unauthorized"}, status=401)
        cam_id = request.match_info["camera_id"]
        body = await request.json()
        for cam in CAMERAS:
            if cam["id"] == cam_id:
                cam.update(body)
                return web.json_response(cam)
        return web.json_response({"error": "not found"}, status=404)

    app.router.add_get("/api/health", health)
    app.router.add_get("/api/cameras", cameras)
    app.router.add_get("/api/cameras/{camera_id}", get_camera)
    app.router.add_put("/api/cameras/{camera_id}", update_camera)
    return app
