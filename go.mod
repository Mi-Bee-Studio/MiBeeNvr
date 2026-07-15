module github.com/Mi-Bee-Studio/MiBeeNvr

go 1.26.2

require (
	github.com/0x524a/onvif-go v1.1.5
	github.com/Eyevinn/hi264 v0.10.0
	github.com/abema/go-mp4 v1.7.1
	github.com/bluenviron/gohlslib/v2 v2.4.0
	github.com/bluenviron/gortmplib v0.4.0
	github.com/bluenviron/gortsplib/v5 v5.6.1
	github.com/bluenviron/mediacommon/v2 v2.9.1
	github.com/datarhei/gosrt v0.11.0
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/fclairamb/ftpserverlib v0.32.1
	github.com/go-chi/chi/v5 v5.3.1
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/pion/dtls/v3 v3.1.5
	github.com/pion/interceptor v0.1.45
	github.com/pion/rtp v1.10.3
	github.com/pion/webrtc/v4 v4.2.16
	github.com/prometheus/client_golang v1.23.2
	github.com/prometheus/client_model v0.6.2
	github.com/spf13/afero v1.15.0
	github.com/stretchr/testify v1.11.1
	golang.org/x/crypto v0.53.0
	golang.org/x/net v0.56.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.53.0
)

require (
	github.com/Eyevinn/mp4ff v0.50.0 // indirect
	github.com/asticode/go-astikit v0.59.0 // indirect
	github.com/asticode/go-astits v1.15.0 // indirect
	github.com/benburkert/openpgp v0.0.0-20160410205803-c2471f86866c // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pion/datachannel v1.6.2 // indirect
	github.com/pion/ice/v4 v4.2.7 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.1.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.17 // indirect
	github.com/pion/sctp v1.10.3 // indirect
	github.com/pion/sdp/v3 v3.0.19 // indirect
	github.com/pion/srtp/v3 v3.0.12 // indirect
	github.com/pion/stun/v3 v3.1.6 // indirect
	github.com/pion/transport/v4 v4.0.2 // indirect
	github.com/pion/turn/v5 v5.0.12 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/prometheus/common v0.69.0 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	golang.org/x/sync v0.21.0 // indirect
	golang.org/x/sys v0.46.0 // indirect
	golang.org/x/text v0.39.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
	modernc.org/libc v1.74.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)

// Use our Mi-Bee-Studio fork of onvif-go, which:
// 1. Extends fixLocalhostURL to also rewrite stale service XAddrs after a camera
//    IP change (DHCP reassignment). Upstream only fixes loopback addresses;
//    without this, rediscovery finds the camera at its new IP but every service
//    call still hits the old, unreachable IP advertised in GetCapabilities.
// 2. Adds clock-skew-aware WS-Security digest (v1.1.6): SetClockSkew on the
//    Client applies the device's time offset to the UsernameToken digest's
//    Created timestamp, fixing Hikvision auth failures caused by clock divergence.
replace github.com/0x524a/onvif-go => github.com/Mi-Bee-Studio/onvif-go v1.1.6
