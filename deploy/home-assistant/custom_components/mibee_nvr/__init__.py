"""MiBee NVR integration: hub device + per-camera entities."""

from __future__ import annotations

import logging

from homeassistant.config_entries import ConfigEntry
from homeassistant.const import CONF_HOST, CONF_PASSWORD, CONF_PORT, CONF_USERNAME
from homeassistant.core import HomeAssistant
from homeassistant.exceptions import ConfigEntryNotReady
from homeassistant.helpers import device_registry as dr

from .api import NVRAuthError, NVRClient, NVRConnectionError, NVRError
from .const import (
    CONF_RTSP_PORT,
    CONF_SCAN_INTERVAL,
    CONF_VERIFY_SSL,
    DEFAULT_RTSP_PORT,
    DEFAULT_SCAN_INTERVAL,
    DEFAULT_VERIFY_SSL,
    DOMAIN,
)
from .coordinator import NVRCoordinator

_LOGGER = logging.getLogger(__name__)

PLATFORMS: list[str] = ["binary_sensor", "camera", "switch"]


async def async_setup_entry(hass: HomeAssistant, entry: ConfigEntry) -> bool:
    hass.data.setdefault(DOMAIN, {})

    client = NVRClient(
        host=entry.data[CONF_HOST],
        port=entry.data[CONF_PORT],
        username=entry.data[CONF_USERNAME],
        password=entry.data[CONF_PASSWORD],
        verify_ssl=entry.data.get(CONF_VERIFY_SSL, DEFAULT_VERIFY_SSL),
    )
    try:
        health = await client.async_validate()
    except NVRAuthError as err:
        raise ConfigEntryNotReady(f"Authentication failed: {err}") from err
    except (NVRConnectionError, NVRError) as err:
        raise ConfigEntryNotReady(f"Cannot connect: {err}") from err

    scan_interval = entry.options.get(
        CONF_SCAN_INTERVAL, entry.data.get(CONF_SCAN_INTERVAL, DEFAULT_SCAN_INTERVAL)
    )
    coordinator = NVRCoordinator(hass, client, scan_interval)
    await coordinator.async_config_entry_first_refresh()

    nvr_device_id = str(health["device_id"])
    hass.data[DOMAIN][entry.entry_id] = {
        "client": client,
        "coordinator": coordinator,
        "device_id": nvr_device_id,
        "rtsp_port": entry.options.get(
            CONF_RTSP_PORT, entry.data.get(CONF_RTSP_PORT, DEFAULT_RTSP_PORT)
        ),
        "version": health.get("version", "unknown"),
    }

    device_registry = dr.async_get(hass)
    device_registry.async_get_or_create(
        config_entry_id=entry.entry_id,
        identifiers={(DOMAIN, nvr_device_id)},
        name=f"MiBee NVR ({entry.data[CONF_HOST]})",
        manufacturer="Mi-Bee Studio",
        model="MiBee NVR",
        sw_version=health.get("version"),
        configuration_url=f"http://{entry.data[CONF_HOST]}:{entry.data[CONF_PORT]}",
    )
    # Per-camera devices link to the hub so entities group per camera.
    for camera in health["cameras"]:
        if camera.get("id"):
            device_registry.async_get_or_create(
                config_entry_id=entry.entry_id,
                identifiers={(DOMAIN, str(camera["id"]))},
                name=str(camera.get("name") or camera["id"]),
                manufacturer="Mi-Bee Studio",
                model=f"NVR camera ({camera.get('protocol', 'unknown')})",
                via_device=(DOMAIN, nvr_device_id),
            )

    await hass.config_entries.async_forward_platforms(entry, PLATFORMS)
    return True


async def async_unload_entry(hass: HomeAssistant, entry: ConfigEntry) -> bool:
    unload_ok = await hass.config_entries.async_unload_platforms(entry, PLATFORMS)
    if unload_ok and (data := hass.data[DOMAIN].pop(entry.entry_id, None)):
        await data["client"].close()
    return unload_ok
