"""Unit tests for the NVR REST client against the fake NVR server."""

from __future__ import annotations

import pytest
import pytest_asyncio
from aiohttp.test_utils import TestServer

from custom_components.mibee_nvr.api import (
    NVRAuthError,
    NVRClient,
    NVRConnectionError,
    NVRNotFoundError,
)

from .fake_nvr import PASSWORD, USERNAME, build_app


@pytest_asyncio.fixture
async def server() -> TestServer:
    server = TestServer(build_app())
    await server.start_server()
    yield server
    await server.close()


def make_client(server: TestServer, password: str = PASSWORD) -> NVRClient:
    return NVRClient(
        host="127.0.0.1",
        port=server.port,
        username=USERNAME,
        password=password,
        verify_ssl=False,
    )


@pytest.mark.asyncio
async def test_health_is_public(server: TestServer) -> None:
    client = make_client(server, password="wrong-password-health-still-works")
    try:
        health = await client.async_health()
    finally:
        await client.close()
    assert health["status"] == "ok"
    assert health["device_id"] == "nvr-test-device"


@pytest.mark.asyncio
async def test_cameras_requires_auth(server: TestServer) -> None:
    client = make_client(server, password="nope")
    try:
        with pytest.raises(NVRAuthError):
            await client.async_cameras()
    finally:
        await client.close()


@pytest.mark.asyncio
async def test_cameras_list(server: TestServer) -> None:
    client = make_client(server)
    try:
        cameras = await client.async_cameras()
    finally:
        await client.close()
    assert {c["id"] for c in cameras} == {"front-door", "yard-esp32", "garage"}


@pytest.mark.asyncio
async def test_update_camera_roundtrip(server: TestServer) -> None:
    client = make_client(server)
    try:
        updated = await client.async_update_camera("garage", {"recording_enabled": True})
        assert updated["recording_enabled"] is True
        again = await client.async_camera("garage")
        assert again["recording_enabled"] is True
    finally:
        await client.close()


@pytest.mark.asyncio
async def test_unknown_camera_404(server: TestServer) -> None:
    client = make_client(server)
    try:
        with pytest.raises(NVRNotFoundError):
            await client.async_camera("no-such-camera")
    finally:
        await client.close()


@pytest.mark.asyncio
async def test_connection_error_on_closed_port() -> None:
    # Port 1 on localhost: nothing listens there in CI.
    client = NVRClient("127.0.0.1", 1, USERNAME, PASSWORD)
    try:
        with pytest.raises(NVRConnectionError):
            await client.async_health()
    finally:
        await client.close()


@pytest.mark.asyncio
async def test_url_builders() -> None:
    client = NVRClient("192.0.2.10", 9090, USERNAME, PASSWORD)
    try:
        assert client.base_url == "http://192.0.2.10:9090"
        assert client.mjpeg_url("yard") == "http://192.0.2.10:9090/api/cameras/yard/stream.mjpeg"
        assert (
            client.latest_frame_url("yard")
            == "http://192.0.2.10:9090/api/cameras/yard/latest-frame"
        )
        assert client.rtsp_url("yard", 8554) == "rtsp://192.0.2.10:8554/yard"
    finally:
        await client.close()
