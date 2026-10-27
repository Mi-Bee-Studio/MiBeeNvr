package onvif

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"

	onvifgo "github.com/mickeyzzc/onvif-go/v2/onvif"
)

var imagingLogger = slogx.Component("onvif-imaging")

// ImagingControllerImpl implements ImagingController by delegating to onvif-go's
// imaging service. The service endpoint (advertised imaging XAddr, with the
// device endpoint as fallback for single-entry firmwares, MiBeeNvr#1075) and
// the auth ladder are resolved by the library client.
type ImagingControllerImpl struct {
	client           *onvifgo.Client
	videoSourceToken string
	mu               sync.Mutex
}

// NewImagingController creates an ImagingController backed by an onvif-go client.
// videoSourceToken is the token imaging operations require — NOT the profile
// token (see the camera manager for how it is derived).
func NewImagingController(client *onvifgo.Client, videoSourceToken string) *ImagingControllerImpl {
	return &ImagingControllerImpl{client: client, videoSourceToken: videoSourceToken}
}

// GetImagingSettings returns the device's current imaging settings.
func (c *ImagingControllerImpl) GetImagingSettings(ctx context.Context) (*ImagingSettings, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return nil, fmt.Errorf("get imaging settings failed: no ONVIF client")
	}

	settings, err := c.client.Imaging().GetImagingSettings(ctx, c.videoSourceToken)
	if err != nil {
		return nil, fmt.Errorf("get imaging settings failed: %w", err)
	}
	return mapImagingSettings(settings), nil
}

// SetImagingSettings applies imaging parameter changes.
func (c *ImagingControllerImpl) SetImagingSettings(ctx context.Context, settings ImagingSettings) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return fmt.Errorf("set imaging settings failed: no ONVIF client")
	}

	if err := c.client.Imaging().SetImagingSettings(ctx, c.videoSourceToken, toOnvifImagingSettings(settings), false); err != nil {
		return fmt.Errorf("set imaging settings failed: %w", err)
	}

	imagingLogger.Info("imaging settings applied", "video_source_token", c.videoSourceToken)
	return nil
}

// GetImagingOptions returns supported parameter ranges.
func (c *ImagingControllerImpl) GetImagingOptions(ctx context.Context) (*ImagingOptions, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.client == nil {
		return nil, fmt.Errorf("get imaging options failed: no ONVIF client")
	}

	options, err := c.client.Imaging().GetOptions(ctx, c.videoSourceToken)
	if err != nil {
		return nil, fmt.Errorf("get imaging options failed: %w", err)
	}
	return mapImagingOptions(options), nil
}

// Compile-time interface check.
var _ ImagingController = (*ImagingControllerImpl)(nil)

// --- Type mapping ---

// onvifImagingModeToLower maps ONVIF mode enums ("AUTO"/"MANUAL") to the
// lowercase form the NVR API carries.
func onvifImagingModeToLower(mode string) string {
	return strings.ToLower(mode)
}

func mapImagingSettings(s *onvifgo.ImagingSettings) *ImagingSettings {
	result := &ImagingSettings{}
	if s == nil {
		return result
	}
	if s.Brightness != nil {
		result.Brightness = *s.Brightness
	}
	if s.Contrast != nil {
		result.Contrast = *s.Contrast
	}
	if s.ColorSaturation != nil {
		result.Saturation = *s.ColorSaturation
	}
	if s.Sharpness != nil {
		result.Sharpness = *s.Sharpness
	}
	if s.Exposure != nil {
		result.Exposure = ExposureSettings{
			Mode:         onvifImagingModeToLower(s.Exposure.Mode),
			ExposureTime: s.Exposure.ExposureTime,
			Gain:         s.Exposure.Gain,
		}
	}
	if s.WhiteBalance != nil {
		result.WhiteBalance = WhiteBalanceSettings{
			Mode:   onvifImagingModeToLower(s.WhiteBalance.Mode),
			CrGain: s.WhiteBalance.CrGain,
			CbGain: s.WhiteBalance.CbGain,
		}
	}
	return result
}

func toOnvifImagingSettings(s ImagingSettings) *onvifgo.ImagingSettings {
	exposureMode := strings.ToUpper(s.Exposure.Mode)
	if exposureMode != "MANUAL" {
		exposureMode = "AUTO"
	}
	wbMode := strings.ToUpper(s.WhiteBalance.Mode)
	if wbMode != "MANUAL" {
		wbMode = "AUTO"
	}
	// ONVIF SetImagingSettings carries CrGain (red) and CbGain (blue) channel
	// gains — they are independent values and must not be forced equal. Legacy
	// callers may have set only ColorTemperature; preserve that behavior by
	// using it for both gains when the explicit gains are unset.
	crGain, cbGain := s.WhiteBalance.CrGain, s.WhiteBalance.CbGain
	if crGain == 0 && cbGain == 0 {
		crGain, cbGain = s.WhiteBalance.ColorTemperature, s.WhiteBalance.ColorTemperature
	}

	return &onvifgo.ImagingSettings{
		Brightness:      &s.Brightness,
		Contrast:        &s.Contrast,
		ColorSaturation: &s.Saturation,
		Sharpness:       &s.Sharpness,
		Exposure: &onvifgo.Exposure{
			Mode:         exposureMode,
			ExposureTime: s.Exposure.ExposureTime,
			Gain:         s.Exposure.Gain,
		},
		WhiteBalance: &onvifgo.WhiteBalance{
			Mode:   wbMode,
			CrGain: crGain,
			CbGain: cbGain,
		},
	}
}

func mapImagingOptions(o *onvifgo.ImagingOptions) *ImagingOptions {
	result := &ImagingOptions{}
	if o == nil {
		return result
	}
	mapRange := func(r *onvifgo.FloatRange) *Range {
		if r == nil {
			return nil
		}
		return &Range{Min: r.Min, Max: r.Max}
	}
	result.Brightness = mapRange(o.Brightness)
	result.Contrast = mapRange(o.Contrast)
	result.Saturation = mapRange(o.ColorSaturation)
	result.Sharpness = mapRange(o.Sharpness)
	if o.Exposure != nil {
		exposure := &ExposureOptions{Mode: o.Exposure.Mode}
		if o.Exposure.MinExposureTime != nil && o.Exposure.MaxExposureTime != nil {
			exposure.ExposureTime = &Range{
				Min: o.Exposure.MinExposureTime.Min,
				Max: o.Exposure.MaxExposureTime.Max,
			}
		}
		if o.Exposure.MinGain != nil && o.Exposure.MaxGain != nil {
			exposure.Gain = &Range{
				Min: o.Exposure.MinGain.Min,
				Max: o.Exposure.MaxGain.Max,
			}
		}
		result.Exposure = exposure
	}
	if o.WhiteBalance != nil {
		result.WhiteBalance = &WhiteBalanceOptions{Mode: o.WhiteBalance.Mode}
	}
	return result
}
