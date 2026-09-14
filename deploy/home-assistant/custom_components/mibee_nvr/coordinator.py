"""DataUpdateCoordinator polling the NVR camera list."""

from __future__ import annotations

import logging
from datetime import timedelta

from homeassistant.core import HomeAssistant
from homeassistant.helpers.update_coordinator import DataUpdateCoordinator, UpdateFailed

from .api import NVRClient, NVRError
from .const import DOMAIN, MIN_SCAN_INTERVAL
from .models import NVRCamera, parse_cameras

_LOGGER = logging.getLogger(__name__)


class NVRCoordinator(DataUpdateCoordinator[dict[str, NVRCamera]]):
    """Keep the camera list fresh; entities derive from it."""

    def __init__(self, hass: HomeAssistant, client: NVRClient, scan_interval: int) -> None:
        super().__init__(
            hass,
            _LOGGER,
            name=DOMAIN,
            update_interval=timedelta(seconds=max(MIN_SCAN_INTERVAL, scan_interval)),
        )
        self.client = client

    async def _async_update_data(self) -> dict[str, NVRCamera]:
        try:
            raw = await self.client.async_cameras()
        except NVRError as err:
            raise UpdateFailed(f"NVR update failed: {err}") from err
        return parse_cameras(raw)
