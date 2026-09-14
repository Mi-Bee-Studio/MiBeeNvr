"""Async REST client for the MiBee NVR API.

This module must stay free of homeassistant imports so it can be unit
tested with a plain aiohttp test server.
"""

from __future__ import annotations

from typing import Any

import aiohttp


class NVRError(Exception):
    """Base error talking to the NVR."""


class NVRConnectionError(NVRError):
    """The NVR is unreachable or timed out."""


class NVRAuthError(NVRError):
    """Credentials rejected (HTTP 401/403)."""


class NVRNotFoundError(NVRError):
    """Requested resource does not exist (HTTP 404)."""


class NVRClient:
    """Thin async client over the NVR REST API (BasicAuth)."""

    def __init__(
        self,
        host: str,
        port: int,
        username: str,
        password: str,
        verify_ssl: bool = False,
        session: aiohttp.ClientSession | None = None,
        timeout: float = 10.0,
    ) -> None:
        self.host = host
        self.port = port
        self.username = username
        self.password = password
        self.verify_ssl = verify_ssl
        self._session = session
        self._timeout = aiohttp.ClientTimeout(total=timeout)
        self._owns_session = False

    @property
    def base_url(self) -> str:
        return f"http://{self.host}:{self.port}"

    def _ensure_session(self) -> aiohttp.ClientSession:
        if self._session is None:
            self._session = aiohttp.ClientSession()
            self._owns_session = True
        return self._session

    async def close(self) -> None:
        if self._owns_session and self._session is not None:
            await self._session.close()
            self._session = None
            self._owns_session = False

    # -- URL builders -------------------------------------------------

    def camera_url(self, camera_id: str) -> str:
        return f"{self.base_url}/api/cameras/{camera_id}"

    def mjpeg_url(self, camera_id: str) -> str:
        return f"{self.base_url}/api/cameras/{camera_id}/stream.mjpeg"

    def latest_frame_url(self, camera_id: str) -> str:
        return f"{self.base_url}/api/cameras/{camera_id}/latest-frame"

    def rtsp_url(self, camera_id: str, rtsp_port: int) -> str:
        return f"rtsp://{self.host}:{rtsp_port}/{camera_id}"

    # -- REST helpers ---------------------------------------------------

    async def _request(
        self,
        method: str,
        path: str,
        json_body: dict[str, Any] | None = None,
        auth: bool = True,
    ) -> Any:
        url = f"{self.base_url}{path}"
        session = self._ensure_session()
        try:
            async with session.request(
                method,
                url,
                json=json_body,
                auth=aiohttp.BasicAuth(self.username, self.password) if auth else None,
                ssl=None if self.verify_ssl else False,
                timeout=self._timeout,
            ) as resp:
                if resp.status in (401, 403):
                    raise NVRAuthError(f"Authentication failed ({resp.status})")
                if resp.status == 404:
                    raise NVRNotFoundError(path)
                resp.raise_for_status()
                if resp.content_type == "application/json":
                    return await resp.json()
                return await resp.read()
        except aiohttp.ClientResponseError as err:
            raise NVRError(f"NVR returned {err.status}") from err
        except TimeoutError as err:
            raise NVRConnectionError(f"Timeout talking to {self.host}:{self.port}") from err
        except aiohttp.ClientConnectionError as err:
            raise NVRConnectionError(f"Cannot reach {self.host}:{self.port}") from err

    async def async_health(self) -> dict[str, Any]:
        """GET /api/health (public endpoint; includes device_id and version)."""
        result = await self._request("GET", "/api/health", auth=False)
        if not isinstance(result, dict):
            raise NVRError("Unexpected /api/health response")
        return result

    async def async_cameras(self) -> list[dict[str, Any]]:
        """GET /api/cameras — the full camera list."""
        result = await self._request("GET", "/api/cameras")
        if isinstance(result, dict):
            result = result.get("cameras", [])
        if not isinstance(result, list):
            raise NVRError("Unexpected /api/cameras response")
        return result

    async def async_camera(self, camera_id: str) -> dict[str, Any]:
        """GET /api/cameras/{id}."""
        result = await self._request("GET", f"/api/cameras/{camera_id}")
        if not isinstance(result, dict):
            raise NVRError("Unexpected camera response")
        return result

    async def async_update_camera(self, camera_id: str, payload: dict[str, Any]) -> dict[str, Any]:
        """PUT /api/cameras/{id} — partial update (e.g. recording_enabled)."""
        result = await self._request("PUT", f"/api/cameras/{camera_id}", json_body=payload)
        if not isinstance(result, dict):
            raise NVRError("Unexpected camera update response")
        return result

    async def async_validate(self) -> dict[str, Any]:
        """Probe used by the config flow: health first, then auth'd cameras.

        Returns {"device_id": ..., "version": ..., "cameras": [...]}.
        Raises NVRConnectionError / NVRAuthError on failure.
        """
        health = await self.async_health()
        cameras = await self.async_cameras()
        return {
            "device_id": health.get("device_id") or health.get("hostname") or self.host,
            "version": health.get("version", "unknown"),
            "cameras": cameras,
        }
