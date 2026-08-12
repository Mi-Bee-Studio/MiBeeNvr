package recorder

import (
	"fmt"
	"os"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model/nalutil"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/muxer"
)

func detectCodec(au [][]byte, encoding string) string {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		firstByte := nalu[0]
		if naluType := firstByte & 0x1F; naluType == 5 || naluType == 7 || naluType == 8 {
			return "h264"
		}
		if naluType := (firstByte >> 1) & 0x3F; naluType == 19 || naluType == 20 || naluType == 32 || naluType == 33 || naluType == 34 {
			return "h265"
		}
	}
	if encoding != "" {
		return encoding
	}
	return ""
}

func updateParamSetsH264(au [][]byte, currentSPS, currentPPS []byte) (newSPS, newPPS []byte, changed bool) {
	sps, pps := nalutil.ExtractParamSetsH264(au)
	if sps != nil {
		newSPS = append([]byte(nil), sps...)
		if currentSPS != nil && !nalutil.EqualParamSets(currentSPS, sps) {
			changed = true
		}
	}
	if pps != nil {
		newPPS = append([]byte(nil), pps...)
		if currentPPS != nil && !nalutil.EqualParamSets(currentPPS, pps) {
			changed = true
		}
	}
	return newSPS, newPPS, changed
}

func updateParamSetsH265(au [][]byte, currentVPS, currentSPS, currentPPS []byte) (newVPS, newSPS, newPPS []byte, changed bool) {
	vps, sps, pps := nalutil.ExtractParamSetsH265(au)
	if vps != nil {
		newVPS = append([]byte(nil), vps...)
		if currentVPS != nil && !nalutil.EqualParamSets(currentVPS, vps) {
			changed = true
		}
	}
	if sps != nil {
		newSPS = append([]byte(nil), sps...)
		if currentSPS != nil && !nalutil.EqualParamSets(currentSPS, sps) {
			changed = true
		}
	}
	if pps != nil {
		newPPS = append([]byte(nil), pps...)
		if currentPPS != nil && !nalutil.EqualParamSets(currentPPS, pps) {
			changed = true
		}
	}
	return newVPS, newSPS, newPPS, changed
}

func prepareBroadcastAU(au [][]byte, isIDR bool, codecType string, sps, pps, vps []byte) [][]byte {
	if !isIDR {
		return au
	}
	if codecType == "h264" && sps != nil && pps != nil {
		broadcastAU := make([][]byte, 0, len(au)+2)
		broadcastAU = append(broadcastAU, sps, pps)
		broadcastAU = append(broadcastAU, au...)
		return broadcastAU
	}
	if codecType == "h265" && vps != nil && sps != nil && pps != nil {
		broadcastAU := make([][]byte, 0, len(au)+3)
		broadcastAU = append(broadcastAU, vps, sps, pps)
		broadcastAU = append(broadcastAU, au...)
		return broadcastAU
	}
	return au
}

func findVCLNALU(au [][]byte, codecType string) []byte {
	for _, nalu := range au {
		if len(nalu) == 0 {
			continue
		}
		firstByte := nalu[0]
		if codecType == "h264" {
			naluType := firstByte & 0x1F
			if naluType == 1 || naluType == 5 {
				return nalu
			}
		} else if codecType == "h265" {
			naluType := (firstByte >> 1) & 0x3F
			if naluType < 32 {
				return nalu
			}
		}
	}
	return nil
}

func createMuxer(cameraID, codecType string, sps, pps, vps []byte) (*muxer.MP4Muxer, int, string, string, error) {
	segmentDir := os.TempDir()
	timestamp := time.Now().Format("20060102_150405")
	tempPath := segmentDir + "/" + cameraID + "_" + timestamp + ".tmp"
	finalPath := segmentDir + "/" + cameraID + "_" + timestamp + ".mp4"
	newMux := muxer.NewMP4Muxer(tempPath)
	var newTrackID int
	var err error
	if codecType == "h264" {
		newTrackID, err = newMux.AddH264Track(sps, pps)
	} else if codecType == "h265" {
		newTrackID, err = newMux.AddH265Track(vps, sps, pps)
	} else {
		err = fmt.Errorf("codec type not detected yet")
	}
	if err != nil {
		os.Remove(tempPath)
		return nil, 0, "", "", err
	}
	return newMux, newTrackID, tempPath, finalPath, nil
}

func closeMuxer(muxer *muxer.MP4Muxer, tempPath, finalPath string, cameraID string) {
	if muxer == nil {
		return
	}
	if err := muxer.Close(); err != nil {
		gb28181Logger.Error("failed to close muxer", "camera_id", cameraID, "error", err)
		if tempPath != "" {
			os.Remove(tempPath)
		}
		return
	}
	if tempPath != "" && finalPath != "" {
		if err := os.Rename(tempPath, finalPath); err != nil {
			gb28181Logger.Error("failed to rename segment", "camera_id", cameraID, "error", err)
		}
	}
}
