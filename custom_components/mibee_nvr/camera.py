"""Camera platform: MJPEG passthrough for JPEG-family, RTSP for H.264/H.265.

Mirrors the documented YAML approach (docs/home-assistant.md Options A / A'):
- JPEG-family encodings → HA MjpegCamera against /api/cameras/{id}/stream.mjpeg
- H.264/H.265 → stream_source from the NVR RTSP output rtsp://host:8554/{id}
"""

from __future__ import annotations

import logging

from homeassistant.components import camera
from homeassistant.components.mjpeg.camera import MjpegCamera
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers import entity_platform
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .api import NVRClient
from .const import ATTR_CAMERA_ID, ATTR_ENCODING, ATTR_PROTOCOL, ATTR_STATUS, DOMAIN
from .coordinator import NVRCoordinator
from .models import NVRCamera

_LOGGER = logging.getLogger(__name__)


async def async_setup_entry(
    hass: HomeAssistant,
    entry: ConfigEntry,
    async_add_entities: entity_platform.AddEntitiesCallback,
) -> None:
    data = hass.data[DOMAIN][entry.entry_id]
    coordinator: NVRCoordinator = data["coordinator"]
    client: NVRClient = data["client"]
    rtsp_port: int = data["rtsp_port"]
    hub_device_id: str = data["device_id"]

    entities: list[camera.Camera] = []
    for cam in coordinator.data.values():
        device_info = DeviceInfo(
            identifiers={(DOMAIN, cam.id)},
            via_device=(DOMAIN, hub_device_id),
        )
        if cam.is_jpeg_family:
            entities.append(MiBeeMjpegCamera(coordinator, client, cam, device_info))
        else:
            entities.append(MiBeeStreamCamera(coordinator, client, cam, rtsp_port, device_info))
    async_add_entities(entities)


class MiBeeCameraBase(CoordinatorEntity[NVRCoordinator]):
    """Shared plumbing: unique id, name, status attributes."""

    _attr_has_entity_name = False

    def __init__(
        self,
        coordinator: NVRCoordinator,
        cam: NVRCamera,
        device_info: DeviceInfo,
    ) -> None:
        super().__init__(coordinator)
        self._cam_id = cam.id
        self._attr_unique_id = f"{cam.id}_camera"
        self._attr_name = cam.name
        self._attr_device_info = device_info

    @property
    def cam(self) -> NVRCamera | None:
        return self.coordinator.data.get(self._cam_id)

    @property
    def extra_state_attributes(self) -> dict[str, str]:
        cam = self.cam
        if cam is None:
            return {}
        return {
            ATTR_CAMERA_ID: cam.id,
            ATTR_STATUS: cam.status,
            ATTR_PROTOCOL: cam.protocol,
            ATTR_ENCODING: cam.encoding,
        }


class MiBeeMjpegCamera(MiBeeCameraBase, MjpegCamera):
    """JPEG-family camera via the NVR MJPEG passthrough (auth required)."""

    def __init__(
        self,
        coordinator: NVRCoordinator,
        client: NVRClient,
        cam: NVRCamera,
        device_info: DeviceInfo,
    ) -> None:
        MiBeeCameraBase.__init__(self, coordinator, cam, device_info)
        self._username = client.username
        self._password = client.password
        self._mjpeg_url = client.mjpeg_url(cam.id)
        self._still_image_url = client.latest_frame_url(cam.id)
        self._verify_ssl = client.verify_ssl

    @property
    def mjpeg_url(self) -> str:
        return self._mjpeg_url

    @property
    def still_image_url(self) -> str:
        return self._still_image_url

    @property
    def username(self) -> str | None:
        return self._username

    @property
    def password(self) -> str | None:
        return self._password

    @property
    def verify_ssl(self) -> bool:
        return self._verify_ssl

    @property
    def supported_features(self) -> int:
        return camera.CameraEntityFeature(0)


class MiBeeStreamCamera(MiBeeCameraBase, camera.Camera):
    """H.264/H.265 camera via the NVR RTSP output (HA stream component)."""

    def __init__(
        self,
        coordinator: NVRCoordinator,
        client: NVRClient,
        cam: NVRCamera,
        rtsp_port: int,
        device_info: DeviceInfo,
    ) -> None:
        MiBeeCameraBase.__init__(self, coordinator, cam, device_info)
        self._stream_source = client.rtsp_url(cam.id, rtsp_port)
        self._still_image_url = client.latest_frame_url(cam.id)
        self._attr_supported_features = camera.CameraEntityFeature.STREAM

    @property
    def stream_source(self) -> str | None:
        return self._stream_source

    async def async_camera_image(
        self, width: int | None = None, height: int | None = None
    ) -> bytes | None:
        """Latest frame; 404 (no FFmpeg on the NVR host) yields no image."""
        client: NVRClient = self.coordinator.client
        try:
            data = await client._request("GET", f"/api/cameras/{self._cam_id}/latest-frame")
        except Exception:  # noqa: BLE001 - a thumbnail must never break the entity
            return None
        return data if isinstance(data, bytes) else None
