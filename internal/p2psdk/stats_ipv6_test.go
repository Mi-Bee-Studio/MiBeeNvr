package peer

import (
	"testing"

	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
)

// IPv6 能力（2026-10-09）：nominated ICE candidate pair 的地址与 IP 版本
// 必须进 StatsSnapshot——v6 直连真正落地时，日志/巡检要能一眼识别
// 「这一对走的是 v6」，否则能力上了也观测不到。

func TestSnapshotFromReportNominatedPairAddresses(t *testing.T) {
	report := webrtc.StatsReport{
		"L1": webrtc.ICECandidateStats{
			ID:            "L1",
			CandidateType: webrtc.ICECandidateTypeHost,
			IP:            "240e:9400:100::10",
			Port:          40000,
		},
		"R1": webrtc.ICECandidateStats{
			ID:            "R1",
			CandidateType: webrtc.ICECandidateTypeHost,
			IP:            "240e:9400:200::20",
			Port:          40001,
		},
		"P1": webrtc.ICECandidatePairStats{
			ID:                   "P1",
			Nominated:            true,
			LocalCandidateID:     "L1",
			RemoteCandidateID:    "R1",
			CurrentRoundTripTime: 0.021,
		},
	}
	snap := snapshotFromReport(report)
	assert.Equal(t, 0.021, snap.ICERTTSeconds)
	assert.Equal(t, "240e:9400:100::10", snap.ICELocalCandidateAddress)
	assert.Equal(t, "240e:9400:200::20", snap.ICERemoteCandidateAddress)
	assert.Equal(t, "6", snap.ICEIPVersion, "v6 地址对应版本=6")
}

func TestSnapshotFromReportIPv4Pair(t *testing.T) {
	report := webrtc.StatsReport{
		"L1": webrtc.ICECandidateStats{
			ID:            "L1",
			CandidateType: webrtc.ICECandidateTypeSrflx,
			IP:            "116.22.2.230",
			Port:          51000,
		},
		"R1": webrtc.ICECandidateStats{
			ID:            "R1",
			CandidateType: webrtc.ICECandidateTypeSrflx,
			IP:            "61.140.196.84",
			Port:          52000,
		},
		"P1": webrtc.ICECandidatePairStats{
			ID:                "P1",
			Nominated:         false, // 未 nominated——不采用
			LocalCandidateID:  "L1",
			RemoteCandidateID: "R1",
		},
	}
	snap := snapshotFromReport(report)
	assert.Empty(t, snap.ICELocalCandidateAddress, "未 nominated 的对不提取")
	assert.Empty(t, snap.ICEIPVersion)
}

func TestIPVersionOf(t *testing.T) {
	assert.Equal(t, "6", ipVersionOf("240e:9400:100::10"))
	assert.Equal(t, "4", ipVersionOf("116.22.2.230"))
	assert.Equal(t, "", ipVersionOf(""))
}
