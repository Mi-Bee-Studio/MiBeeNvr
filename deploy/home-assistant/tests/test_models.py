"""Tests for camera parsing / state derivation (no HA imports)."""

from __future__ import annotations

from custom_components.mibee_nvr.models import NVRCamera, parse_cameras


def test_parse_full_list() -> None:
    cameras = parse_cameras(
        [
            {
                "id": "front-door",
                "name": "Front Door",
                "status": "Recording",
                "protocol": "rtsp",
                "encoding": "H264",
                "recording_enabled": True,
            },
            {"id": "yard", "name": "Yard", "status": "reconnecting"},
        ]
    )
    assert set(cameras) == {"front-door", "yard"}
    front = cameras["front-door"]
    assert front.is_recording is True
    assert front.is_jpeg_family is False
    assert front.recording_enabled is True
    yard = cameras["yard"]
    assert yard.is_recording is False
    assert yard.encoding == ""
    assert yard.recording_enabled is None


def test_jpeg_family_detection() -> None:
    for encoding in ("mjpeg", "JPEG", "jpeg", "mjpa"):
        cam = NVRCamera("a", "A", "recording", "http", encoding, True)
        assert cam.is_jpeg_family, encoding
    for encoding in ("h264", "h265", "hevc", ""):
        cam = NVRCamera("a", "A", "recording", "rtsp", encoding, True)
        assert not cam.is_jpeg_family, encoding


def test_name_falls_back_to_id() -> None:
    cam = NVRCamera.from_api({"id": "cam-1", "status": ""})
    assert cam.name == "cam-1"
    assert cam.status == ""


def test_parse_cameras_skips_idless_rows() -> None:
    cameras = parse_cameras([{"id": "", "name": "broken"}, {"id": "ok"}])
    assert set(cameras) == {"ok"}
