# Xiaomi Camera Setup

## Overview

MiBee NVR supports Xiaomi cameras via the CS2 P2P protocol. This allows you to connect Xiaomi cloud cameras to your NVR system without direct network access to the cameras themselves.

## Prerequisites

- Xiaomi account with registered cameras
- Cameras bound to your Xiaomi account
- Network access to Xiaomi cloud services

## Supported Models

| Model | Identifier | Notes |
|-------|------------|-------|
| Xiaomi C200 | `chuangmi.camera.046c04` | Full support |
| Xiaomi C300 | `chuangmi.camera.72ac1` | Full support |
| Xiaofang | `isa.camera.isc5c1` | Full support |
| Loock V2 | `loock.cateye.v02` | Full support |
| Dafang | `isa.camera.df3` | **Not supported** - requires TUTK protocol |

**Note**: Dafang cameras use the TUTK protocol which is not implemented in the current version. Only CS2 protocol cameras are supported.

## Setup Steps

### Web UI Method (Recommended)

1. Open NVR Web UI → Cameras page
2. Expand "Xiaomi Device Discovery" section
3. Enter Xiaomi account credentials
4. Click "Sign In"
5. Select cameras from device list
6. Click "Add to NVR" for each camera

### Manual Configuration

Alternatively, you can configure Xiaomi cameras manually by editing the configuration file:

```yaml
xiaomi:
  user_id: "123456789"
  token: "your_passToken_here"
  region: "cn"

cameras:
  - id: "xiaomi_c200"
    name: "Xiaomi C200"
    protocol: "xiaomi"
    encoding: "h264"
    did: "device_id_here"
    vendor: "cs2"
    enabled: true
```

**Configuration Fields**:

- `user_id`: Your Xiaomi user ID (obtained after first login)
- `token`: Xiaomi passToken (obtained via `/api/xiaomi/auth` endpoint)
- `region`: Region code (default: "cn", also supports "sg", "de", etc.)

## Security Note

**Tokens are stored in plaintext in the configuration file.** Ensure proper file permissions:

```bash
chmod 600 mibee-nvr.yaml
```

Token encryption is planned for future versions. Consider using a secure configuration location with restricted access.

## Troubleshooting

### Common Issues

#### "Unsupported vendor" Error
- **Cause**: Only CS2 protocol is supported in version 1
- **Solution**: Ensure your camera model is in the supported list above
- **Note**: Dafang cameras are not supported due to TUTK protocol requirement

#### "Auth Failed" Error
- **Cause**: Invalid credentials or account requires captcha/2FA
- **Solutions**:
  - Verify Xiaomi account credentials
  - Ensure account doesn't require captcha verification
  - Check if account has two-factor authentication enabled
  - Try with a fresh Xiaomi account if possible

#### Camera Not Listed
- **Cause**: Camera not bound to Xiaomi account or offline
- **Solutions**:
  - Ensure camera is online and connected to Xiaomi cloud
  - Verify camera is bound to your Xiaomi account in the Mi Home app
  - Check network connectivity to Xiaomi services
  - Try refreshing the device list

#### Recording Fails
- **Cause**: Network connectivity issues or camera offline
- **Solutions**:
  - Check network connectivity to camera
  - Verify camera is online in Mi Home app
  - Check NVR system logs for specific error messages
  - Try re-authenticating with Xiaomi cloud

#### Recording Stops After ~20 Minutes
- **Cause**: CS2 P2P TCP connection hangs when camera stops sending data (no idle timeout)
- **Solution**: Update to the latest version which adds read deadlines and independent TCP keepalive pings
- **Note**: The fix adds a 30-second idle timeout to detect dead connections and triggers automatic reconnection

### Getting Xiaomi Credentials

If you need to obtain your Xiaomi credentials manually:

1. Use the `/api/xiaomi/auth` endpoint to authenticate
2. The response will contain `user_id` and `pass_token`
3. Use these values in your configuration

### Network Requirements

- Ensure NVR can reach Xiaomi cloud services
- Firewalls must allow connections to `openapi.io.mi.com`
- Xiaomi cloud services may be blocked in some regions

### Performance Considerations

- Xiaomi cameras add network latency compared to direct RTSP
- CS2 protocol compression may affect video quality
- Consider local RTSP cameras for lower latency needs

## Next Steps

Once your Xiaomi cameras are configured, you can:
- View live streams via the Web UI
- Access recorded segments
- Configure retention policies
- Set up alerts and notifications

For additional support, check the [MiBee NVR documentation](../getting-started.md) or create an issue on the GitHub repository.