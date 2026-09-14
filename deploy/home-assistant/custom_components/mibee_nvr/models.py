"""Parsed camera model — homeassistant-free so it stays unit-testable."""

from __future__ import annotations

from dataclasses import dataclass

from .const import JPEG_ENCODINGS, RECORDING_STATUS


@dataclass(frozen=True)
class NVRCamera:
    """One camera as reported by GET /api/cameras."""

    id: str
    name: str
    status: str
    protocol: str
    encoding: str
    recording_enabled: bool | None

    @property
    def is_recording(self) -> bool:
        return self.status == RECORDING_STATUS

    @property
    def is_jpeg_family(self) -> bool:
        """Cameras served by the NVR MJPEG passthrough (HA MjpegCamera)."""
        return self.encoding.lower() in JPEG_ENCODINGS

    @classmethod
    def from_api(cls, data: dict) -> NVRCamera:
        recording = data.get("recording_enabled")
        if not isinstance(recording, bool):
            recording = None
        return cls(
            id=str(data.get("id", "")),
            name=str(data.get("name") or data.get("id", "unknown")),
            status=str(data.get("status", "") or "").lower(),
            protocol=str(data.get("protocol", "") or ""),
            encoding=str(data.get("encoding", "") or "").lower(),
            recording_enabled=recording,
        )


def parse_cameras(raw: list[dict]) -> dict[str, NVRCamera]:
    """Parse the /api/cameras payload into {camera_id: NVRCamera}."""
    return {c.id: c for c in (NVRCamera.from_api(item) for item in raw) if c.id}
