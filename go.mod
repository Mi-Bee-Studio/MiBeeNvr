module github.com/Mi-Bee-Studio/MiBeeNvr

go 1.26.2

require (
	github.com/Eyevinn/hi264 v0.10.0
	github.com/abema/go-mp4 v1.7.3
	github.com/bluenviron/gohlslib/v2 v2.4.4
	github.com/bluenviron/gortmplib v1.0.2
	github.com/bluenviron/gortsplib/v5 v5.6.5
	github.com/bluenviron/mediacommon/v2 v2.9.4
	github.com/datarhei/gosrt v0.11.0
	github.com/eclipse/paho.mqtt.golang v1.5.1
	github.com/fclairamb/ftpserverlib v0.32.4
	github.com/ghettovoice/gosip v0.0.0-20260603143348-d1f3b494c69a
	github.com/go-chi/chi/v5 v5.3.2
	github.com/google/uuid v1.6.0
	github.com/gorilla/websocket v1.5.3
	github.com/klauspost/compress v1.20.0
	github.com/pion/interceptor v0.1.48
	github.com/pion/rtp v1.10.5
	github.com/pion/webrtc/v4 v4.2.20
	github.com/prometheus/client_golang v1.24.1
	github.com/prometheus/client_model v0.6.3
	github.com/spf13/afero v1.15.0
	github.com/stretchr/testify v1.12.1
	golang.org/x/crypto v0.57.0
	golang.org/x/mod v0.41.0
	golang.org/x/net v0.59.0
	gopkg.in/yaml.v3 v3.0.1
	modernc.org/sqlite v1.59.0
)

require go.uber.org/goleak v1.3.0

require (
	github.com/Mi-Bee-Studio/MiBeeP2PServer/sdk/go v0.0.0-20260920051840-0bb9dc8d873c
	github.com/emmansun/gmsm v0.44.1
)

require github.com/pion/stun/v4 v4.0.0 // indirect

require (
	github.com/mickeyzzc/gb28181-go v0.10.0
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/text v0.42.0 // indirect
)

require (
	github.com/Eyevinn/mp4ff v0.50.0 // indirect
	github.com/asticode/go-astikit v0.59.0 // indirect
	github.com/asticode/go-astits v1.16.0 // indirect
	github.com/benburkert/openpgp v0.0.0-20160410205803-c2471f86866c // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/discoviking/fsm v0.0.0-20150126104936-f4a273feecca // indirect
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/hashicorp/mdns v1.0.7
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mgutz/ansi v0.0.0-20200706080929-d51e80ef957d // indirect
	github.com/mickeyzzc/onvif-go/v2 v2.1.0
	github.com/miekg/dns v1.1.73 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pion/datachannel v1.6.2 // indirect
	github.com/pion/dtls/v3 v3.1.8 // indirect
	github.com/pion/ice/v4 v4.4.2 // indirect
	github.com/pion/logging v0.2.4 // indirect
	github.com/pion/mdns/v2 v2.2.0 // indirect
	github.com/pion/randutil v0.1.0 // indirect
	github.com/pion/rtcp v1.2.17 // indirect
	github.com/pion/sctp v1.11.1 // indirect
	github.com/pion/sdp/v3 v3.0.20 // indirect
	github.com/pion/srtp/v3 v3.0.13 // indirect
	github.com/pion/transport/v4 v4.1.1 // indirect
	github.com/pion/turn/v5 v5.1.0 // indirect
	github.com/prometheus/common v0.71.0 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/satori/go.uuid v1.2.1-0.20181028125025-b2ce2384e17b // indirect
	github.com/sirupsen/logrus v1.10.2 // indirect
	github.com/tevino/abool v1.2.0 // indirect
	github.com/wlynxg/anet v0.0.5 // indirect
	github.com/x-cray/logrus-prefixed-formatter v0.5.2 // indirect
	golang.org/x/sync v0.23.0
	golang.org/x/sys v0.48.0
	golang.org/x/term v0.46.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
	modernc.org/libc v1.76.0 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)

// onvif-go is our own maintained continuation of 0x524a/onvif-go
// (github.com/mickeyzzc/onvif-go/v2, client at /v2/onvif, v2.0.0-rc2+): service-facade API, capability
// XAddr repair, clock-skew-aware WS-Security digest. No replace needed.
