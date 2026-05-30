package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	onvifgo "github.com/0x524a/onvif-go"
)

var imagingLogger = slog.Default().With("component", "onvif-imaging")

// ImagingControllerImpl implements ImagingController by delegating to onvif-go's imaging service
// via raw SOAP requests.
type ImagingControllerImpl struct {
	client           *onvifgo.Client
	profileToken     string
	imagingEndpoint  string // may differ from device endpoint
	username         string
	password         string
	mu               sync.Mutex
}

// NewImagingController creates an ImagingController backed by an onvif-go client.
// profileToken is used for SOAP imaging requests (most cameras accept profile token).
func NewImagingController(client *onvifgo.Client, profileToken string) *ImagingControllerImpl {
	return &ImagingControllerImpl{
		client:       client,
		profileToken: profileToken,
	}
}

// SetImagingEndpoint overrides the SOAP endpoint for imaging requests.
// If empty, the default onvif-go client endpoint is used.
func (c *ImagingControllerImpl) SetImagingEndpoint(endpoint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.imagingEndpoint = endpoint
}

// SetCredentials sets the username/password for raw SOAP requests.
func (c *ImagingControllerImpl) SetCredentials(username, password string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.username = username
	c.password = password
}

// GetImagingSettings returns current imaging settings via raw SOAP.
func (c *ImagingControllerImpl) GetImagingSettings(ctx context.Context) (*ImagingSettings, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	endpoint := c.imagingEndpoint
	if endpoint == "" {
		return nil, fmt.Errorf("imaging endpoint not configured")
	}

	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:timg="http://www.onvif.org/ver20/imaging/wsdl"
 xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <timg:GetImagingSettings>
      <timg:VideoSourceToken>%s</timg:VideoSourceToken>
    </timg:GetImagingSettings>
  </s:Body>
</s:Envelope>`, c.profileToken)

	respBody, err := c.doRawSOAP(ctx, endpoint, soapBody)
	if err != nil {
		return nil, fmt.Errorf("get imaging settings failed: %w", err)
	}

	return parseImagingSettingsResponse(respBody)
}

// SetImagingSettings applies imaging parameter changes via raw SOAP.
func (c *ImagingControllerImpl) SetImagingSettings(ctx context.Context, settings ImagingSettings) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	endpoint := c.imagingEndpoint
	if endpoint == "" {
		return fmt.Errorf("imaging endpoint not configured")
	}

	// Build the ImagingSettings XML block
	exposureXML := buildExposureSettingsXML(settings.Exposure)
	wbXML := buildWhiteBalanceSettingsXML(settings.WhiteBalance)

	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:timg="http://www.onvif.org/ver20/imaging/wsdl"
 xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <timg:SetImagingSettings>
      <timg:VideoSourceToken>%s</timg:VideoSourceToken>
      <timg:ImagingSettings>
        <tt:Brightness>%s</tt:Brightness>
        <tt:ColorSaturation>%s</tt:ColorSaturation>
        <tt:Contrast>%s</tt:Contrast>
        <tt:Sharpness>%s</tt:Sharpness>
        %s
        %s
      </timg:ImagingSettings>
    </timg:SetImagingSettings>
  </s:Body>
</s:Envelope>`,
		c.profileToken,
		fmt.Sprintf("%f", settings.Brightness),
		fmt.Sprintf("%f", settings.Saturation),
		fmt.Sprintf("%f", settings.Contrast),
		fmt.Sprintf("%f", settings.Sharpness),
		exposureXML,
		wbXML,
	)

	_, err := c.doRawSOAP(ctx, endpoint, soapBody)
	if err != nil {
		return fmt.Errorf("set imaging settings failed: %w", err)
	}

	imagingLogger.Info("imaging settings applied", "profile_token", c.profileToken)
	return nil
}

// GetImagingOptions returns supported parameter ranges via raw SOAP.
func (c *ImagingControllerImpl) GetImagingOptions(ctx context.Context) (*ImagingOptions, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	endpoint := c.imagingEndpoint
	if endpoint == "" {
		return nil, fmt.Errorf("imaging endpoint not configured")
	}

	soapBody := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:timg="http://www.onvif.org/ver20/imaging/wsdl"
 xmlns:tt="http://www.onvif.org/ver10/schema">
  <s:Body>
    <timg:GetOptions>
      <timg:VideoSourceToken>%s</timg:VideoSourceToken>
    </timg:GetOptions>
  </s:Body>
</s:Envelope>`, c.profileToken)

	respBody, err := c.doRawSOAP(ctx, endpoint, soapBody)
	if err != nil {
		return nil, fmt.Errorf("get imaging options failed: %w", err)
	}

	return parseImagingOptionsResponse(respBody)
}

// doRawSOAP sends a raw SOAP request and returns the response body.
func (c *ImagingControllerImpl) doRawSOAP(ctx context.Context, endpoint, soapBody string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, strings.NewReader(soapBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml")
	if c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SOAP request failed with status %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	return body, nil
}

// --- XML response parsing ---

// imagingSettingsResponse represents the SOAP response for GetImagingSettings.
type imagingSettingsResponse struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		XMLName xml.Name `xml:"Body"`
		GetImagingSettingsResponse struct {
			XMLName         xml.Name `xml:"GetImagingSettingsResponse"`
			ImagingSettings struct {
				Brightness     float64 `xml:"Brightness"`
				ColorSaturation float64 `xml:"ColorSaturation"`
				Contrast       float64 `xml:"Contrast"`
				Sharpness      float64 `xml:"Sharpness"`
			} `xml:"ImagingSettings"`
		} `xml:"GetImagingSettingsResponse"`
	} `xml:"Body"`
}

func parseImagingSettingsResponse(body []byte) (*ImagingSettings, error) {
	var envelope imagingSettingsResponse
	if err := xml.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse imaging settings response: %w", err)
	}
	s := envelope.Body.GetImagingSettingsResponse.ImagingSettings
	return &ImagingSettings{
		Brightness:  s.Brightness,
		Saturation:  s.ColorSaturation,
		Contrast:    s.Contrast,
		Sharpness:   s.Sharpness,
	}, nil
}

// imagingOptionsResponse represents the SOAP response for GetOptions.
type imagingOptionsResponse struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		XMLName xml.Name `xml:"Body"`
		GetOptionsResponse struct {
			XMLName       xml.Name `xml:"GetOptionsResponse"`
			ImagingOptions struct {
				Brightness struct {
					Min float64 `xml:"Min"`
					Max float64 `xml:"Max"`
				} `xml:"Brightness"`
				ColorSaturation struct {
					Min float64 `xml:"Min"`
					Max float64 `xml:"Max"`
				} `xml:"ColorSaturation"`
				Contrast struct {
					Min float64 `xml:"Min"`
					Max float64 `xml:"Max"`
				} `xml:"Contrast"`
				Sharpness struct {
					Min float64 `xml:"Min"`
					Max float64 `xml:"Max"`
				} `xml:"Sharpness"`
			} `xml:"ImagingOptions"`
		} `xml:"GetOptionsResponse"`
	} `xml:"Body"`
}

func parseImagingOptionsResponse(body []byte) (*ImagingOptions, error) {
	var envelope imagingOptionsResponse
	if err := xml.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse imaging options response: %w", err)
	}
	o := envelope.Body.GetOptionsResponse.ImagingOptions
	return &ImagingOptions{
		Brightness: &Range{Min: o.Brightness.Min, Max: o.Brightness.Max},
		Saturation: &Range{Min: o.ColorSaturation.Min, Max: o.ColorSaturation.Max},
		Contrast:   &Range{Min: o.Contrast.Min, Max: o.Contrast.Max},
		Sharpness:  &Range{Min: o.Sharpness.Min, Max: o.Sharpness.Max},
	}, nil
}

// --- XML helpers ---

func buildExposureSettingsXML(exp ExposureSettings) string {
	mode := "AUTO"
	if exp.Mode == "manual" {
		mode = "MANUAL"
	}
	return fmt.Sprintf(`<tt:Exposure>
  <tt:Mode>%s</tt:Mode>
  <tt:ExposureTime>%f</tt:ExposureTime>
  <tt:Gain>%f</tt:Gain>
</tt:Exposure>`, mode, exp.ExposureTime, exp.Gain)
}

func buildWhiteBalanceSettingsXML(wb WhiteBalanceSettings) string {
	mode := "AUTO"
	if wb.Mode == "manual" {
		mode = "MANUAL"
	}
	return fmt.Sprintf(`<tt:WhiteBalance>
  <tt:Mode>%s</tt:Mode>
  <tt:CrGain>%f</tt:CrGain>
  <tt:CbGain>%f</tt:CbGain>
</tt:WhiteBalance>`, mode, wb.ColorTemperature, wb.ColorTemperature)
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Compile-time interface check.
var _ ImagingController = (*ImagingControllerImpl)(nil)
