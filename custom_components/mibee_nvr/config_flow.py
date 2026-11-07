"""Config flow for MiBee NVR (manual or mDNS _mibee-nvr._tcp discovery)."""

from __future__ import annotations

from typing import Any

import voluptuous as vol
from homeassistant import config_entries
from homeassistant.const import CONF_HOST, CONF_PASSWORD, CONF_PORT, CONF_USERNAME
from homeassistant.core import callback
from homeassistant.data_entry_flow import FlowResult

from .api import NVRAuthError, NVRClient, NVRConnectionError, NVRError
from .const import (
    CONF_RTSP_PORT,
    CONF_SCAN_INTERVAL,
    CONF_VERIFY_SSL,
    DEFAULT_PORT,
    DEFAULT_RTSP_PORT,
    DEFAULT_SCAN_INTERVAL,
    DEFAULT_VERIFY_SSL,
    DOMAIN,
)

ZEROCONF_TYPE = "_mibee-nvr._tcp.local."


class MiBeeNVRConfigFlow(config_entries.ConfigFlow, domain=DOMAIN):
    """Handle the MiBee NVR config flow."""

    VERSION = 1

    def __init__(self) -> None:
        self._discovered_host: str | None = None
        self._discovered_port: int | None = None

    async def async_step_user(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        if user_input is not None:
            return await self._async_validate_and_create(user_input)

        return self.async_show_form(
            step_id="user",
            data_schema=vol.Schema(
                {
                    vol.Required(CONF_HOST): str,
                    vol.Required(CONF_PORT, default=DEFAULT_PORT): int,
                    vol.Required(CONF_USERNAME): str,
                    vol.Required(CONF_PASSWORD): str,
                    vol.Optional(CONF_VERIFY_SSL, default=DEFAULT_VERIFY_SSL): bool,
                    vol.Optional(CONF_RTSP_PORT, default=DEFAULT_RTSP_PORT): int,
                }
            ),
        )

    async def async_step_zeroconf(self, discovery_info: dict[str, Any]) -> FlowResult:
        """mDNS _mibee-nvr._tcp advertisement — prefill host/port."""
        self._discovered_host = discovery_info.get("host") or discovery_info.get(
            "hostname", ""
        ).rstrip(".")
        self._discovered_port = int(discovery_info.get("port") or DEFAULT_PORT)
        if not self._discovered_host:
            return self.async_abort(reason="no_devices_found")
        await self.async_set_unique_id(f"{self._discovered_host}:{self._discovered_port}")
        self._abort_if_unique_id_configured()
        return await self.async_step_confirm_discovery()

    async def async_step_confirm_discovery(
        self, user_input: dict[str, Any] | None = None
    ) -> FlowResult:
        if user_input is not None:
            user_input[CONF_HOST] = self._discovered_host
            user_input[CONF_PORT] = self._discovered_port
            return await self._async_validate_and_create(user_input)

        return self.async_show_form(
            step_id="confirm_discovery",
            description_placeholders={
                "host": self._discovered_host or "",
                "port": str(self._discovered_port or DEFAULT_PORT),
            },
            data_schema=vol.Schema(
                {
                    vol.Required(CONF_USERNAME): str,
                    vol.Required(CONF_PASSWORD): str,
                    vol.Optional(CONF_VERIFY_SSL, default=DEFAULT_VERIFY_SSL): bool,
                    vol.Optional(CONF_RTSP_PORT, default=DEFAULT_RTSP_PORT): int,
                }
            ),
        )

    async def _async_validate_and_create(self, user_input: dict[str, Any]) -> FlowResult:
        client = NVRClient(
            host=user_input[CONF_HOST],
            port=user_input[CONF_PORT],
            username=user_input[CONF_USERNAME],
            password=user_input[CONF_PASSWORD],
            verify_ssl=user_input.get(CONF_VERIFY_SSL, DEFAULT_VERIFY_SSL),
        )
        try:
            info = await client.async_validate()
        except NVRAuthError:
            return self.async_show_form(
                step_id="user" if self._discovered_host is None else "confirm_discovery",
                errors={"base": "invalid_auth"},
                description_placeholders={"host": user_input[CONF_HOST]},
            )
        except (NVRConnectionError, NVRError):
            return self.async_show_form(
                step_id="user" if self._discovered_host is None else "confirm_discovery",
                errors={"base": "cannot_connect"},
                description_placeholders={"host": user_input[CONF_HOST]},
            )
        finally:
            await client.close()

        await self.async_set_unique_id(str(info["device_id"]))
        self._abort_if_unique_id_configured()
        return self.async_create_entry(
            title=f"MiBee NVR ({user_input[CONF_HOST]})",
            data={
                CONF_HOST: user_input[CONF_HOST],
                CONF_PORT: user_input[CONF_PORT],
                CONF_USERNAME: user_input[CONF_USERNAME],
                CONF_PASSWORD: user_input[CONF_PASSWORD],
                CONF_VERIFY_SSL: user_input.get(CONF_VERIFY_SSL, DEFAULT_VERIFY_SSL),
                CONF_RTSP_PORT: user_input.get(CONF_RTSP_PORT, DEFAULT_RTSP_PORT),
            },
        )

    @staticmethod
    @callback
    def async_get_options_flow(
        config_entry: config_entries.ConfigEntry,
    ) -> MiBeeNVROptionsFlow:
        return MiBeeNVROptionsFlow()


class MiBeeNVROptionsFlow(config_entries.OptionsFlow):
    """Options: RTSP port and polling interval."""

    async def async_step_init(self, user_input: dict[str, Any] | None = None) -> FlowResult:
        if user_input is not None:
            return self.async_create_entry(title="", data=user_input)

        current_rtsp = self.config_entry.options.get(
            CONF_RTSP_PORT, self.config_entry.data.get(CONF_RTSP_PORT, DEFAULT_RTSP_PORT)
        )
        current_scan = self.config_entry.options.get(
            CONF_SCAN_INTERVAL,
            self.config_entry.data.get(CONF_SCAN_INTERVAL, DEFAULT_SCAN_INTERVAL),
        )
        return self.async_show_form(
            step_id="init",
            data_schema=vol.Schema(
                {
                    vol.Required(CONF_RTSP_PORT, default=current_rtsp): int,
                    vol.Required(CONF_SCAN_INTERVAL, default=current_scan): int,
                }
            ),
        )
