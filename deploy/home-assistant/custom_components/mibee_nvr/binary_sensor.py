"""Binary sensors per camera: Connected (connectivity) and Recording."""

from __future__ import annotations

import logging

from homeassistant.components.binary_sensor import (
    BinarySensorDeviceClass,
    BinarySensorEntity,
)
from homeassistant.config_entries import ConfigEntry
from homeassistant.core import HomeAssistant
from homeassistant.helpers import entity_platform
from homeassistant.helpers.device_registry import DeviceInfo
from homeassistant.helpers.update_coordinator import CoordinatorEntity

from .const import (
    CONNECTED_STATUSES,
    DISCONNECTED_STATUSES,
    DOMAIN,
)
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

    entities: list[BinarySensorEntity] = []
    for cam in coordinator.data.values():
        device_info = DeviceInfo(
            identifiers={(DOMAIN, cam.id)},
            via_device=(DOMAIN, hub_device_id),
        )
        entities.append(MiBeeConnectedSensor(coordinator, cam, device_info))
        entities.append(MiBeeRecordingSensor(coordinator, cam, device_info))
    async_add_entities(entities)


class MiBeeBinarySensorBase(CoordinatorEntity[NVRCoordinator], BinarySensorEntity):
    def __init__(
        self, coordinator: NVRCoordinator, cam: NVRCamera, device_info: DeviceInfo
    ) -> None:
        super().__init__(coordinator)
        self._cam_id = cam.id
        self._attr_device_info = device_info


class MiBeeConnectedSensor(MiBeeBinarySensorBase):
    """Is the camera connected and being pulled by the NVR.

    "stopped" (recording deliberately disabled) reports unavailable —
    connectivity is simply not being exercised in that state.
    """

    _attr_device_class = BinarySensorDeviceClass.CONNECTIVITY

    def __init__(
        self, coordinator: NVRCoordinator, cam: NVRCamera, device_info: DeviceInfo
    ) -> None:
        super().__init__(coordinator, cam, device_info)
        self._attr_unique_id = f"{cam.id}_connected"
        self._attr_name = f"{cam.name} Connected"

    @property
    def is_on(self) -> bool | None:
        cam = self.coordinator.data.get(self._cam_id)
        if cam is None or cam.status not in (CONNECTED_STATUSES | DISCONNECTED_STATUSES):
            return None
        return cam.status in CONNECTED_STATUSES


class MiBeeRecordingSensor(MiBeeBinarySensorBase):
    """Is the camera currently writing segments."""

    def __init__(
        self, coordinator: NVRCoordinator, cam: NVRCamera, device_info: DeviceInfo
    ) -> None:
        super().__init__(coordinator, cam, device_info)
        self._attr_unique_id = f"{cam.id}_recording"
        self._attr_name = f"{cam.name} Recording"
        self._attr_icon = "mdi:record-rec"

    @property
    def is_on(self) -> bool | None:
        cam = self.coordinator.data.get(self._cam_id)
        if cam is None:
            return None
        return cam.is_recording
