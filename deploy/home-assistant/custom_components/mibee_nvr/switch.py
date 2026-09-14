"""Switch per camera: recording_enabled toggle (PUT /api/cameras/{id})."""

from __future__ import annotations

import logging

from homeassistant.components.switch import SwitchEntity
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers import entity_platform
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .const import DOMAIN
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
    hub_device_id: str = data["device_id"]

    entities = []
    for cam in coordinator.data.values():
        device_info = DeviceInfo(
            identifiers={(DOMAIN, cam.id)},
            via_device=(DOMAIN, hub_device_id),
        )
        entities.append(MiBeeRecordingSwitch(coordinator, cam, device_info))
    async_add_entities(entities)


class MiBeeRecordingSwitch(CoordinatorEntity[NVRCoordinator], SwitchEntity):
    """Toggle the camera's recording_enabled flag."""

    _attr_icon = "mdi:video"

    def __init__(
        self, coordinator: NVRCoordinator, cam: NVRCamera, device_info: DeviceInfo
    ) -> None:
        super().__init__(coordinator)
        self._cam_id = cam.id
        self._attr_unique_id = f"{cam.id}_recording_enabled"
        self._attr_name = f"{cam.name} Recording enabled"
        self._attr_device_info = device_info

    @property
    def is_on(self) -> bool | None:
        cam = self.coordinator.data.get(self._cam_id)
        if cam is None or cam.recording_enabled is None:
            return None
        return cam.recording_enabled

    async def async_turn_on(self, **kwargs) -> None:  # noqa: ANN003
        await self._set_recording(True)

    async def async_turn_off(self, **kwargs) -> None:  # noqa: ANN003
        await self._set_recording(False)

    async def _set_recording(self, enabled: bool) -> None:
        await self.coordinator.client.async_update_camera(
            self._cam_id, {"recording_enabled": enabled}
        )
        await self.coordinator.async_request_refresh()
