"""Import validation: every HA-facing module must load against the real
Home Assistant package pinned in requirements_test.txt.

Catches API drift (renamed/moved symbols, changed signatures) at CI time
instead of at first install.
"""

from __future__ import annotations

import importlib
import json
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
        (Path(__file__).parent.parent / "custom_components/mibee_nvr/manifest.json").read_text(
            encoding="utf-8"
        )
    )
    assert manifest["domain"] == "mibee_nvr"
    assert manifest["config_flow"] is True
    assert manifest["version"]
    assert "stream" in manifest["dependencies"]
