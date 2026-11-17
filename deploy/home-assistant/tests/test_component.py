"""Import validation: every HA-facing module must load against the real
Home Assistant package pinned in requirements_test.txt.

Catches API drift (renamed/moved symbols, changed signatures) at CI time
instead of at first install.
"""

from __future__ import annotations

import importlib
import inspect
import json
import types
from pathlib import Path

import pytest

MODULES = [
    "custom_components.mibee_nvr.const",
    "custom_components.mibee_nvr.api",
    "custom_components.mibee_nvr.models",
    "custom_components.mibee_nvr.coordinator",
    "custom_components.mibee_nvr.config_flow",
    "custom_components.mibee_nvr.camera",
    "custom_components.mibee_nvr.binary_sensor",
    "custom_components.mibee_nvr.switch",
    "custom_components.mibee_nvr",
]


@pytest.mark.parametrize("module", MODULES)
def test_module_imports(module: str) -> None:
    importlib.import_module(module)


def test_manifest_is_valid() -> None:
    manifest = json.loads(
        (
            Path(__file__).parent.parent.parent.parent / "custom_components/mibee_nvr/manifest.json"
        ).read_text(encoding="utf-8")
    )
    assert manifest["domain"] == "mibee_nvr"
    assert manifest["config_flow"] is True
    assert manifest["version"]
    assert "stream" in manifest["dependencies"]


def test_stream_source_stays_an_async_method() -> None:
    """Our stream_source override must match the base-class async-method contract.

    `Camera.stream_source` is `async def` on every HA version (verified
    2025.1.0 → dev). A `@property` override shadows the method with a plain
    str: the WebRTC provider scan that runs eagerly at entity add (2025.4+)
    then crashes with `TypeError: 'str' object is not callable`, and stream
    creation fails the same way on versions that call it lazily. Found by the
    2025.10 container smoke (#1161) — 16 of 20 camera entities failed to add
    while config-entry setup looked green.
    """
    from homeassistant.components import camera as ha_camera

    from custom_components.mibee_nvr.camera import MiBeeStreamCamera

    base = inspect.getattr_static(ha_camera.Camera, "stream_source")
    ours = inspect.getattr_static(MiBeeStreamCamera, "stream_source")
    assert isinstance(base, types.FunctionType) and inspect.iscoroutinefunction(base)
    assert isinstance(ours, types.FunctionType), "stream_source must be a method, not a property"
    assert inspect.iscoroutinefunction(ours), "stream_source must be declared async like the base"
