#KJ|# Camera Brand Compatibility Guide
#KM|
#RW|MiBee NVR supports a wide range of IP cameras through various protocols including RTSP (H.264/H.265/MJPEG), HTTP JPEG, and ONVIF. This guide provides comprehensive compatibility information for popular camera brands, including supported protocols, configuration examples, and troubleshooting tips.
#SY|
#PX|## Quick Start (Top 3 Brands)
#JW|
#QW|### Hikvision
#HX|
#VY|```yaml
#BB|cameras:
#RW|  - id: "hikvision_front_door"
#SX|    name: "Front Door - Hikvision"
#MK|    protocol: "rtsp"
#BB|    encoding: "h264"
#HM|    url: "rtsp://admin:password123@192.168.1.100:554/Streaming/Channels/101"
#SJ|    enabled: true
#KB|```
#XS|
#YJ|1. **Access Camera**: Find camera IP address via Hikvision iVMS-4200 or web interface
#VT|2. **Enable RTSP**: Ensure RTSP streaming is enabled in camera web interface (usually under Network → Advanced)
#RW|3. **Configure**: Use the URL above with your camera's IP and credentials
#QQ|
#XQ|### Dahua
#BX|
#VY|```yaml
#BB|cameras:
#RW|  - id: "dahua_driveway"
#SX|    name: "Driveway - Dahua"
#MK|    protocol: "rtsp"
#BB|    encoding: "h264"
#HM|    url: "rtsp://admin:admin@192.168.1.101:554/cam/realmonitor?channel=1&subtype=0"
#SJ|    enabled: true
#KB|```
#XS|
#YJ|1. **Access Camera**: Find camera IP address via Dahua SmartPSS or web interface
#VT|2. **Enable RTSP**: Enable RTSP streaming in camera settings (usually under Configuration → Network → Stream)
#RW|3. **Configure**: Use the URL above with your camera's IP and credentials
#QQ|
#XQ|### Uniview
#VX|
#VY|```yaml
#BB|cameras:
#RW|  - id: "uniview_parking"
#SX|    name: "Parking - Uniview"
#MK|    protocol: "rtsp"
#BB|    encoding: "h264"
#HM|    url: "rtsp://admin:123456@192.168.1.102:554/unicast/c1/s0/live"
#SJ|    enabled: true
#KB|```
#XS|
#YJ|1. **Access Camera**: Find camera IP address via Uniview iViewer or web interface
#VT|2. **Enable RTSP**: Enable RTSP streaming in camera settings (usually under Network → Streaming)
#RW|3. **Configure**: Use the URL above with your camera's IP and credentials
#QQ|
#QX|## Compatibility Overview
#MB|
#JW|| Tier | Support Level | Protocol | Brands | Notes |
#SX||------|---------------|----------|--------|-------|
#RW|| **Full Support** | ✅ Auto-detect + PTZ | RTSP + ONVIF | Hikvision, Dahua, Uniview, Axis, Bosch, Vivotek, Hanwha, Amcrest, Reolink | Best experience, full features |
#BB|| **Manual Setup** | ⚠️ RTSP only, limited ONVIF | RTSP only | TP-Link VIGI, EZVIZ, ANNKE, Lorex, Swann, Speco | Manual configuration required |
#MK|| **Limited/Special** | 🔧 Special handling needed | Various | Xiaomi, BESDER, Generic, Wyze | Custom setup or limitations |
#BW|
#YY|## Full Support (ONVIF + RTSP)
#WV|
#XS|### 1. Hikvision
#JZ|
#MH|**Models**: DS-2CD2T42WDG1-I, DS-2CD2143G0-I, DS-2CD2343G0-I
#KQ|
#TM|#### RTSP URLs:
#VW|- Main Stream: `rtsp://user:pass@ip:554/Streaming/Channels/101`
#NV|- Sub Stream: `rtsp://user:pass@ip:554/Streaming/Channels/102`
#XS|- Audio Channel: `rtsp://user:pass@ip:554/Streaming/Channels/101_1`
#JQ|
#HT|#### Configuration:
#VY|```yaml
#BB|cameras:
#RW|  - id: "hikvision_main"
#SX|    name: "Hikvision Main Stream"
#MK|    protocol: "rtsp"
#BB|    encoding: "h264"
#HM|    url: "rtsp://admin:password123@192.168.1.100:554/Streaming/Channels/101"
#SJ|    enabled: true
#KB|```
#XS|
#YJ|#### Default Credentials:
#BK|- Username: `admin`
#KQ|- Password: `password123` (default, may vary)
#MS|
#MT|#### Known Issues:
#BK|- Some models require enabling RTSP in web interface first
#KQ|- ONVIF may not work on firmware older than V5.3.x
#MS|- Audio streams sometimes separate from video
#KQ|
#YX|### 2. Dahua
#BY|
#JB|**Models**: IPC-HFW1237S-Z, IPC-HDW2831R-ZS, IPC-HFW5442E-Z
#QW|
#TM|#### RTSP URLs:
#VW|- Channel 1: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`
#NV|- Channel 2: `rtsp://user:pass@ip:554/cam/realmonitor?channel=2&subtype=0`
#XS|- Audio Stream: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=1`
#JQ|
#HT|#### Configuration:
#VY|```yaml
#BB|cameras:
#RW|  - id: "dahua_main"
#SX|    name: "Dahua Main Stream"
#MK|    protocol: "rtsp"
#BB|    encoding: "h264"
#HM|    url: "rtsp://admin:admin@192.168.1.101:554/cam/realmonitor?channel=1&subtype=0"
#SJ|    enabled: true
#KB|```
#XS|
#YJ|#### Default Credentials:
#BK|- Username: `admin`
#KQ|- Password: `admin` (default)
#MS|
#MT|#### Known Issues:
#BK>- Some models use different substream URLs (`subtype=1` for substream)
#KQ>- Audio may need separate configuration
#MS>- ONVIF discovery may take longer than RTSP direct
#KQ>
#YX>### 3. Uniview
#BY>
#JB>**Models**: U-AI1208LBF, U-AI1208LBFZ, U-CV3208ERBU
#QW>
#TM>#### RTSP URLs:
#VW>- Main Stream: `rtsp://user:pass@ip:554/unicast/c1/s0/live`
#NV>- Sub Stream: `rtsp://user:pass@ip:554/unicast/c1/s1/live`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/unicast/c1/s0/live`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "uniview_main"
#SX>    name: "Uniview Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:123456@192.168.1.102:554/unicast/c1/s0/live"
#SJ>    enabled: true
#KB>```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: `123456` (default)
#MS>
#MT>#### Known Issues:
#BK>- Some models require RTSP to be enabled in camera settings
#KQ>- Audio streams may be separate from video
#MS>- ONVIF port may vary (usually 80, but check camera settings)
#KQ>
#YX>### 4. Axis
#BY>
#JB>**Models**: M3045-V, M3054-V, M3065-V
#QW>
#TM>#### RTSP URLs:
#VW>- Main Stream: `rtsp://root:pass@ip:554/axis-media/media.amp`
#NV>- High Quality: `rtsp://root:pass@ip:554/axis-media/media.amp?videoType=1`
#XS>- Audio Stream: `rtsp://root:pass@ip:554/axis-media/media.amp?videoType=3`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "axis_main"
#SX>    name: "Axis Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://root:password@192.168.1.103:554/axis-media/media.amp"
#SJ>    enabled: true
#KB>```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `root`
#KQ>- Password: `pass` (default)
#MS>
#MT>#### Known Issues:
#BK>- Some models require specific authentication methods
#KQ>- Audio streams may need separate configuration
#MS>- Camera firmware updates can change RTSP endpoints
#KQ>
#YX>### 5. Bosch
#BY>
#JB>**Models**: FLEXIDOME 5000I, MIC IP, DINION 6000I
#QW>
#TM>#### RTSP URLs:
#VW>- Video Stream 1: `rtsp://user:pass@ip:554/rtp_video1`
#NV>- Video Stream 2: `rtsp://user:pass@ip:554/rtp_video2`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/rtp_audio1`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "bosch_main"
#SX>    name: "Bosch Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:12345@192.168.1.104:554/rtp_video1"
#SJ>    enabled: true
#KB>```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: `12345` (default)
#MS>
#MT>#### Known Issues:
#BK>- Some models use different port ranges for RTSP
#KQ>- Audio may require separate configuration
#MS>- ONVIF configuration can be complex
#KQ>
#YX>### 6. Vivotek
#BY>
#JB>**Models**: IB8369, IP8332-H, FD8166
#QW>
#TM>#### RTSP URLs:
#VW>- Live Stream: `rtsp://user:pass@ip:554/live.sdp`
#NV>- Main Stream: `rtsp://user:pass@ip:554/h264/ch1/main/av_stream`
#XS>- Sub Stream: `rtsp://user:pass@ip:554/h264/ch1/sub/av_stream`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "vivotek_main"
#SX>    name: "Vivotek Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://root:123456@192.168.1.105:554/live.sdp"
#SJ>    enabled: true
#KB>```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `root`
#KQ>- Password: `123456` (default)
#MS>
#MT>#### Known Issues:
#BK>- Some models require specific RTSP paths
#KQ>- Audio streams may not work with all models
#MS>- ONVIF discovery may need manual configuration
#KQ>
#YX>### 7. Hanwha
#BY>
#JB>**Models**: XNV-6081R, XND-6081R, XNV-8081R
#QW>
#TM>#### RTSP URLs:
#VW>- Profile 1: `rtsp://user:pass@ip:554/profile1/media.smp`
#NV>- Profile 2: `rtsp://user:pass@ip:554/profile2/media.smp`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/profile1/audio`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "hanwha_main"
#SX>    name: "Hanwha Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:123456@192.168.1.106:554/profile1/media.smp"
#SJ>    enabled: true
#KB>```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: `123456` (default)
#MS>
#MT>#### Known Issues:
#BK>- Some models use different profile numbers
#KQ>- Audio streams may require separate configuration
#MS>- ONVIF port varies by model (check camera web interface)
#KQ>
#YX>### 8. Amcrest
#BY>
#JB>**Models**: IP8M-T1179EW, IP5M-T1177EW, IP2M-841EB
#QW>
#TM>#### RTSP URLs:
#VW>- Channel 1: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`
#NV>- Channel 2: `rtsp://user:pass@ip:554/cam/realmonitor?channel=2&subtype=0`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=1`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "amcrest_main"
#SX>    name: "Amcrest Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:admin@192.168.1.107:554/cam/realmonitor?channel=1&subtype=0"
#SJ>    enabled: true
#KB>```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: `admin` (default)
#MS>
#MT>#### Known Issues:
#BK>- Similar to Dahua cameras but with different firmware
#KQ>- Some models use subtypes differently
#MS>- ONVIF discovery works but may be slow
#KQ>
#YX>### 9. Reolink
#BY>
#JB>**Models**: RLC-810A, RLC-510A, RLC-823A
#QW>
#TM>#### RTSP URLs:
#VW>- Main Stream: `rtsp://user:pass@ip:554/h264Preview_01_main`
#NV>- Sub Stream: `rtsp://user:pass@ip:554/h264Preview_01_sub`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/h264Preview_01_main`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "reolink_main"
#SX>    name: "Reolink Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:123456@192.168.1.108:554/h264Preview_01_main"
#SJ>    enabled: true
#KB>```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: `123456` (default)
#MS>
#MT>#### Known Issues:
#BK>- Some models require Reolink-specific authentication
#KQ>- Audio streams may need separate configuration
#MS>- ONVIF discovery works but port may be different (8000)
#KQ>
#YX>## Manual Setup (RTSP Only)
#BY>
#JB>### 10. TP-Link VIGI
#QW>
#JM>**Models**: TC-VR303, TC-VR307, TC-V304GS
#SX>
#TM>#### RTSP URLs:
#VW>- Stream 1: `rtsp://user:pass@ip:554/stream1`
#NV>- Stream 2: `rtsp://user:pass@ip:554/stream2`
#XS>- Sub Stream: `rtsp://user:pass@ip:554/stream1_sub`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "tplink_vigi"
#SX>    name: "TP-Link VIGI"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:admin@192.168.1.109:554/stream1"
#SJ>    enabled: true
#KB>```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: `admin` (default)
#MS>
#MT>#### Known Issues:
#BK>- Limited ONVIF support (may not discover automatically)
#KQ>- Some models require manual URL configuration
#MS>- Audio streams not always available
#KQ>
#YX>### 11. EZVIZ
#BY>
#JB>**Models**: CS-CV248, CS-CV228, CS-CV208
#QW>
#TM>#### RTSP URLs:
#VW>- Channel 1: `rtsp://user:pass@ip:554/h264/ch1/main/av_stream`
#NV>- Channel 2: `rtsp://user:pass@ip:554/h264/ch2/main/av_stream`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/h264/ch1/main/audio`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "ezviz_main"
#SX>    name: "EZVIZ Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:verification_code@192.168.1.110:554/h264/ch1/main/av_stream"
#SJ>    enabled: true
#KB>```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: Verification code from camera sticker
#MS>
#MT>#### Known Issues:
#BK>- No ONVIF support on most models
#KQ>- Password changes frequently (sticker-based)
#MS>- Some models require firmware updates for RTSP support
#KQ>
#YX>### 12. ANNKE
#BY>
#JB>**Models**: 8CH H.265, 4CH H.264, 16CH NVR
#QW>
#TM>#### RTSP URLs:
#VW>- Channel 1: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=0`
#NV>- Channel 2: `rtsp://user:pass@ip:554/cam/realmonitor?channel=2&subtype=0`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/cam/realmonitor?channel=1&subtype=1`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "annke_main"
#SX>    name: "ANNKE Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:admin123@192.168.1.111:554/cam/realmonitor?channel=1&subtype=0"
#SJ>    enabled: true
#KB>```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: `admin123` (default)
#MS>
#MT>#### Known Issues:
#BK>- Dahua OEM, similar URL format to Dahua
#KQ>- ONVIF support varies by model
#MS>- Some models require specific firmware versions
#KQ>
#YX>### 13. Lorex
#BY>
#JB>**Models**: LNB4432, LNC22852B, LNE8832
#QW>
#TM>#### RTSP URLs:
#VW>- Main Stream: `rtsp://user:pass@ip:554/stream`
#NV>- Sub Stream: `rtsp://user:pass@ip:554/stream_sub`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/stream_audio`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "lorex_main"
#SX>    name: "Lorex Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:000000@192.168.1.112:554/stream"
#SJ>    enabled: true
#KB|```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: `000000` or `123456` (varies by model)
#MS>
#MT>#### Known Issues:
#BK>- Dahua/FLIR OEM, RTSP format varies by model
#KQ>- ONVIF support inconsistent across models
#MS>- Some models require specific configuration steps
#KQ>
#YX>### 14. Swann
#BY>
#JB>**Models**: SWPRO-890MS, SWPRO-882MS, SWPRO-848MS
#QW>
#TM>#### RTSP URLs:
#VW>- Main Stream: `rtsp://user:pass@ip:554/stream`
#NV>- Sub Stream: `rtsp://user:pass@ip:554/stream_sub`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/stream_audio`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "swann_main"
#SX>    name: "Swann Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:123456@192.168.1.113:554/stream"
#SJ>    enabled: true
#KB|```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: `123456` (default)
#MS>
#MT>#### Known Issues:
#BK>- Similar to Lorex, varies by model
#KQ>- ONVIF support inconsistent
#MS>- Some models require firmware updates
#KQ>
#YX>### 15. Speco Technologies
#BY>
#JB>**Models**: C128-HV, C828-HV, C168-HV
#QW>
#TM>#### RTSP URLs:
#VW>- Main Stream: `rtsp://user:pass@ip:554/stream`
#NV>- Sub Stream: `rtsp://user:pass@ip:554/stream_sub`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/stream_audio`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "speco_main"
#SX>    name: "Speco Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:1234@192.168.1.114:554/stream"
#SJ>    enabled: true
#KB|```
#XS>
#YJ>#### Default Credentials:
#BK>- Username: `admin`
#KQ>- Password: `1234` (default)
#MS>
#MT>#### Known Issues:
#BK>- Good ONVIF support but RTSP format varies
#KQ>- Some models require manual configuration
#MS>- Documentation can be outdated
#KQ>
#YX>## Limited / Special Cases
#BY>
#JB>### 16. Xiaomi
#QW>
#JM>**Models**: C200, C300, Xiaofang, Dafang
#SX>
#TM>#### Special Setup:
#VW>- Use MiBee NVR's built-in Xiaomi integration, NOT RTSP directly
#NV>- Configure via Web UI → Xiaomi Device Discovery
#XS>- Authentication via Xiaomi cloud services
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>xiaomi:
#RW>  enabled: true
#SX>  user_id: "123456789"
#MK>  token: "your_passToken_here"
#BB>  region: "cn"
#HM|  
#BB|cameras:
#RW>  - id: "xiaomi_c200"
#SX>    name: "Xiaomi C200"
#MK>    protocol: "xiaomi"
#BB>    encoding: "h264"
#HM|    did: "device_id_here"
#SJ|    vendor: "cs2"
#KB|    enabled: true
#BB|```
#XS>
#YJ>#### Known Issues:
#BK>- Not compatible with RTSP directly
#KQ>- Requires Xiaomi cloud connectivity
#MS>- P2P protocol only, no direct IP access
#KQ>
#YX>### 17. BESDER
#BY>
#JB>**Models**: BCS-SR-505, BCS-SR-608, BCS-SR-702
#QW>
#TM>#### RTSP URLs:
#VW>- Main Stream: `rtsp://user:pass@ip:554/stream`
#NV>- Sub Stream: `rtsp://user:pass@ip:554/sub`
#XS>- Audio Stream: `rtsp://user:pass@ip:554/audio`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "besder_main"
#SX>    name: "BESDER Main Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:password@192.168.1.115:554/stream"
#SJ>    enabled: true
#KB|```
#XS>
#YJ>#### Known Issues:
#BK>- Usually ONVIF compatible but quality varies
#KQ>- RTSP format not standardized across models
#MS>- Some models have poor video quality over RTSP
#KQ>
#YX>### 18. Generic RTSP
#BY>
#JB>**Models**: Various Chinese brands, white-label cameras
#QW>
#TM>#### RTSP URLs:
#VW>- Common formats: `rtsp://user:pass@ip:554/stream`
#NV>- Alternative formats: `rtsp://user:pass@ip:554/live`
#XS>- Another option: `rtsp://user:pass@ip:554/h264`
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "generic_rtsp"
#SX>    name: "Generic RTSP Camera"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:password@192.168.1.116:554/stream"
#SJ>    enabled: true
#KB|```
#XS>
#YJ>#### Known Issues:
#BK>- RTSP format varies greatly by manufacturer
#KQ>- Trial and error required for URL format
#MS>- ONVIF discovery may not work
#KQ>
#YX>### 19. Generic ONVIF
#BY>
#JB>**Models**: Various ONVIF-compliant cameras
#QW>
#TM>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "generic_onvif"
#SX>    name: "Generic ONVIF Camera"
#MK>    protocol: "onvif"
#BB>    url: "http://192.168.1.117:80/onvif/device_service"
#HM|    username: "admin"
#SJ|    password: "password"
#KB|    enabled: true
#BB|```
#XS>
#YJ>#### Known Issues:
#BK>- Use MiBee's ONVIF Scan feature for auto-discovery
#KQ>- May require manual configuration if discovery fails
#MS>- ONVIF profiles may vary between models
#KQ>
#YX>### 20. Wyze
#BY>
#JB>**Models**: Wyze Cam v3, Wyze Cam Outdoor, Wyze Cam Pan
#QW>
#TM>#### Special Setup:
#VW>- Limited RTSP support via custom firmware
#NV>- Requires Wyze RTSP fork or equivalent
#XS>- Mostly cloud-based, not ideal for NVR
#JQ>
#HT>#### Configuration:
#VY>```yaml
#BB>cameras:
#RW>  - id: "wyze_rtsp"
#SX>    name: "Wyze RTSP Stream"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:password@192.168.1.118:554/live"
#SJ>    enabled: true
#KB|```
#XS>
#YJ>#### Known Issues:
#BK>- Not officially supported by Wyze
#KQ>- Requires specific firmware modifications
#MS>- Performance can be poor with custom firmware
#KQ>
#YX>## General Configuration Tips
#BY>
#JB>### RTSP URL Testing
#QW>
#JM>Before adding to MiBee NVR, test RTSP URLs with VLC or ffplay:
#SX>
#BV>```bash
#BB># Test RTSP connection
#RW>ffplay rtsp://admin:password@192.168.1.100:554/stream
#SX>
#BB># Test HTTP JPEG
#RW>curl http://admin:password@192.168.1.101:8080/snapshot
#SX>
#BB># Check camera network connectivity
#RW>ping 192.168.1.100
#SX>
#BB># Test ONVIF discovery
#RW>nmap -p 80 192.168.1.102
#BB>```
#XS>
#YJ>### Performance Optimization
#BY>
#JB>For Raspberry Pi users, optimize settings for limited resources:
#QW>
#VY>```yaml
#BB>cameras:
#RW>  - id: "optimized_camera"
#SX>    name: "Optimized Camera"
#MK>    protocol: "rtsp"
#BB>    encoding: "h264"
#HM>    url: "rtsp://admin:password@192.168.1.100:554/stream"
#SJ>    enabled: true
#KB|    # Optimize for Raspberry Pi
#RW>    segment_duration: "30s"
#SX>    hls_max_fps: 15
#RW>    sample_interval: 2
#BB>```
#XS>
#YJ>### Network Considerations
#BY>
#JB>- Ensure cameras and MiBee NVR are on the same network
#QW>- Consider VLAN segmentation for security
#SX>- Use static IP addresses for reliable connections
#RW>- Monitor bandwidth usage for multiple cameras
#BB>
#YJ>### Security Best Practices
#BY>
#JB>- Change default camera passwords
#QW>- Use different credentials for each camera
#SX>- Enable firewall rules to restrict camera access
#RW>- Regular firmware updates for cameras
#BB>- Consider HTTPS for camera web interfaces
#BB>
#YX>## Troubleshooting
#BY>
#JB>### Common RTSP Issues
#QW>
#JM>**"RTSP connection failed"**:
#SX>
#BV>```bash
#BB># Check if camera is online
#RW>ping 192.168.1.100
#SX>
#BB># Test RTSP manually
#RW>ffplay rtsp://admin:password@192.168.1.100:554/stream
#SX>
#BB># Check camera port accessibility
#RW>nc -zv 192.168.1.100 554
#BB>```
#XS>
#YJ>**"No video stream available"**:
#BY>
#JB>- Verify RTSP is enabled in camera settings
#QW>- Check if camera requires authentication
#SX>- Try different stream formats (main/sub)
#RW>- Test URL with VLC to confirm stream works
#BB>
#YJ>### ONVIF Discovery Issues
#BY>
#JB>**Camera not discovered via ONVIF**:
#QW>
#BV>```yaml
#BB># Manual ONVIF configuration
#RW>cameras:
#SX>  - id: "manual_onvif"
#RW>    name: "Manual ONVIF Camera"
#RW>    protocol: "onvif"
#RW>    url: "http://192.168.1.100:80/onvif/device_service"
#RW|    username: "admin"
#RW|    password: "password"
#RW|    enabled: true
#BB>```
#XS>
#YJ>**ONVIF authentication failed**:
#BY>
#JB>- Check ONVIF port (usually 80, but varies)
#QW>- Verify camera supports ONVIF
#SX>- Try different authentication methods
#RW>- Check camera firmware version for ONVIF support
#BB>
#YJ>### Audio Configuration
#BY>
#JB>**No audio in recordings**:
#QW>
#BV>```yaml
#BB># Enable audio in camera configuration
#RW>cameras:
#SX>  - id: "camera_with_audio"
#RW>    name: "Camera with Audio"
#RW>    protocol: "rtsp"
#RW>    encoding: "h264"
#RW>    url: "rtsp://admin:password@192.168.1.100:554/stream"
#RW|    enabled: true
#RW|    # Audio settings
#RW|    enable_audio: true
#RW|    audio_codec: "aac"  # or pcm, g711
#RW|    audio_bitrate: "128k"
#BB>```
#XS>
#YJ>**Audio sync issues**:
#BY>
#JB>- Check if audio stream is separate from video
#QW>- Adjust audio sync settings if available
#SX>- Some cameras don't support audio in RTSP stream
#RW>- Consider separate audio recording if needed
#BB>
#YX>## Support Resources
#BY>
#JB>### Documentation
#QW>
#JM>- [MiBee NVR Configuration Guide](./configuration.md)
#SX>- [MiBee NVR API Reference](./api-reference.md)
#RW>- [MiBee NVR Getting Started](./getting-started.md)
#BB>
#YJ>### Community Support
#BY>
#JB>- GitHub Issues: [MiBee NVR Issues](https://github.com/Mi-Bee-Studio/MiBeeNvr/issues)
#QW>- Discussions: [MiBee NVR Discussions](https://github.com/Mi-Bee-Studio/MiBeeNvr/discussions)
#SX>- Discord: Join our community server for help
#BB>
#YJ>### Professional Support
#BY>
#JB>- Commercial support available for enterprise users
#QW>- Contact us for custom camera integration
#SX>- On-site consultation available for large deployments
#BB>
#YX>## Camera Discovery Tools
#BY>
#JB>### ONVIF Device Manager
#QW>
#JM>Download and install [ONVIF Device Manager](https://www.onvif.org/) to:
#SX>
#RW>- Discover ONVIF cameras on your network
#RW>- Get detailed camera information
#RW>- Test ONVIF compliance
#RW>- Download camera capabilities
#BB>
#YJ>### iSpy
#BY>
#JB>[iSpy](https://www.ispyconnect.com/) can help discover cameras and generate RTSP URLs:
#QW>
#RW>- Automatic camera discovery
#RW>- RTSP URL generation
#RW>- Testing stream compatibility
#RW>- Camera configuration management
#BB>
#YJ>### FFMPEG RTSP URL Tester
#BY>
#JM>Create a simple script to test RTSP URLs:
#QW>
#BV>```bash
#BB>#!/bin/bash
#RW>
#RW># Test RTSP URL function
#RW>test_rtsp_url() {
#RW>    local url="$1"
#RW>    local timeout=10
#RW>    
#RW>    echo "Testing RTSP URL: $url"
#RW>    
#RW>    # Test with timeout
#RW>    timeout $timeout ffmpeg -i "$url" -t 1 -f null - 2>/dev/null && {
#RW>        echo "✅ RTSP URL works: $url"
#RW>        return 0
#RW>    } || {
#RW>        echo "❌ RTSP URL failed: $url"
#RW>        return 1
#RW>    }
#RW>}
#RW>
#RW># Example usage
#RW>test_rtsp_url "rtsp://admin:password@192.168.1.100:554/stream"
#RW>
#RW># Batch test multiple URLs
#RW>for ip in 192.168.1.{100..120}; do
#RW>    test_rtsp_url "rtsp://admin:password@$ip:554/stream"
#RW>done
#BB>```
#XS>
#YJ>### Network Scanning Tools
#BY>
#JB>Use these tools to find cameras on your network:
#QW>
#BV>```bash
#RW>
#RW># Find ONVIF cameras
#RW>nmap -p 80 --open 192.168.1.0/24 | grep -v "Nmap scan"
#RW>
#RW># Find RTSP servers
#RW>nmap -p 554 --open 192.168.1.0/24 | grep -v "Nmap scan"
#RW>
#RW># Find HTTP cameras
#RW>nmap -p 80,8080 --open 192.168.1.0/24 | grep -v "Nmap scan"
#RW>
#RW># Quick camera discovery
#BB>sudo nmap -sV -p 554,80,8080 --open 192.168.1.0/24
#BB>```
#XS>
#YJ>Through comprehensive camera brand compatibility information, this guide helps you successfully integrate various IP cameras with MiBee NVR, ensuring optimal recording performance and reliability for your surveillance system.