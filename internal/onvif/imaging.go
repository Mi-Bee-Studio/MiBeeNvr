package onvif

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	onvifgo "github.com/mickeyzzc/onvif-go"
)

var imagingLogger = slog.Default().With("component", "onvif-imaging")

// ImagingControllerImpl implements ImagingController by delegating to onvif-go's imaging service
// via raw SOAP requests.
type ImagingControllerImpl struct {
	client          *onvifgo.Client
	profileToken    string
	imagingEndpoint string // may differ from device endpoint
	username        string
	password        string
	mu              sync.Mutex
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

	soapBody := fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>
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
//
// Some cameras reject HTTP Basic Auth on the imaging service while accepting
// WS-Security, or vice-versa (firmware-dependent). To handle this uniformly,
// doRawSOAP tries multiple auth strategies and falls back on auth rejection
// (HTTP 401 or a SOAP Fault). The order is:
//
//  1. WS-Security UsernameToken (PasswordText) — injected into the SOAP envelope.
//  2. HTTP Basic Auth — set on the HTTP request.
//  3. No auth — some minimal devices reject all auth headers on imaging.
//
// The first strategy that yields HTTP 200 with a non-Fault body wins. This
// mirrors the fallback chain already used by PTZ/DeviceMgmt (see client.go).
func (c *ImagingControllerImpl) doRawSOAP(ctx context.Context, endpoint, soapBody string) ([]byte, error) {
	// Strategy 1: WS-Security UsernameToken (PasswordText).
	if c.username != "" {
		body, err := c.sendSOAP(ctx, endpoint, soapBody, authWSSecurity)
		if err == nil {
			return body, nil
		}
		if !isAuthError(err) {
			return nil, err // non-auth error, don't try further strategies
		}
		imagingLogger.Debug("WS-Security auth rejected for imaging, trying Basic Auth", "error", err)
	}

	// Strategy 2: HTTP Basic Auth.
	if c.username != "" {
		body, err := c.sendSOAP(ctx, endpoint, soapBody, authBasic)
		if err == nil {
			return body, nil
		}
		if !isAuthError(err) {
			return nil, err
		}
		imagingLogger.Debug("Basic Auth rejected for imaging, trying no auth", "error", err)
	}

	// Strategy 3: No auth (minimal devices that reject all auth headers).
	body, err := c.sendSOAP(ctx, endpoint, soapBody, authNone)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// soapAuthStrategy selects how a SOAP request is authenticated.
type soapAuthStrategy int

const (
	authNone       soapAuthStrategy = iota
	authBasic                       // HTTP Basic Auth header
	authWSSecurity                  // WS-Security UsernameToken in SOAP header
)

// sendSOAP dispatches a single SOAP request using the given auth strategy.
func (c *ImagingControllerImpl) sendSOAP(ctx context.Context, endpoint, soapBody string, auth soapAuthStrategy) ([]byte, error) {
	payload := soapBody
	if auth == authWSSecurity && c.username != "" {
		payload = strings.Replace(soapBody, "<s:Body>", buildWSSecurityHeaderFor(c.username, c.password)+"<s:Body>", 1)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml")
	if auth == authBasic && c.username != "" {
		req.SetBasicAuth(c.username, c.password)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	// HTTP-level auth rejection (401/403) — caller may fall back to another strategy.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("SOAP request failed with status %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SOAP request failed with status %d: %s", resp.StatusCode, truncateStr(string(body), 500))
	}

	// A SOAP Fault inside a 200 response can also indicate auth rejection
	// (some devices return 200 with a Fault body). Detect and surface it so
	// the caller's isAuthError check can trigger a fallback.
	if hasSOAPFault(body) {
		return nil, fmt.Errorf("SOAP Fault: %s", truncateStr(string(body), 500))
	}

	return body, nil
}

// buildWSSecurityHeaderFor builds a WS-Security UsernameToken header (PasswordText)
// for the given credentials. Shared between client.go and imaging.go.
func buildWSSecurityHeaderFor(username, password string) string {
	return `<s:Header><wsse:Security xmlns:wsse="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd" xmlns:wsu="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"><wsse:UsernameToken><wsse:Username>` + username + `</wsse:Username><wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText">` + password + `</wsse:Password></wsse:UsernameToken></wsse:Security></s:Header>`
}

// hasSOAPFault reports whether a SOAP response body contains a Fault element.
func hasSOAPFault(body []byte) bool {
	return bytes.Contains(body, []byte("<s:Fault>")) ||
		bytes.Contains(body, []byte("<Fault>")) ||
		bytes.Contains(body, []byte(":Fault>"))
}

// --- XML response parsing ---

// imagingSettingsResponse represents the SOAP response for GetImagingSettings.
type imagingSettingsResponse struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		XMLName                    xml.Name `xml:"Body"`
		GetImagingSettingsResponse struct {
			XMLName         xml.Name `xml:"GetImagingSettingsResponse"`
			ImagingSettings struct {
				Brightness      float64 `xml:"Brightness"`
				ColorSaturation float64 `xml:"ColorSaturation"`
				Contrast        float64 `xml:"Contrast"`
				Sharpness       float64 `xml:"Sharpness"`
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
		Brightness: s.Brightness,
		Saturation: s.ColorSaturation,
		Contrast:   s.Contrast,
		Sharpness:  s.Sharpness,
	}, nil
}

// imagingOptionsResponse represents the SOAP response for GetOptions.
type imagingOptionsResponse struct {
	XMLName xml.Name `xml:"Envelope"`
	Body    struct {
		XMLName            xml.Name `xml:"Body"`
		GetOptionsResponse struct {
			XMLName        xml.Name `xml:"GetOptionsResponse"`
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
	// ONVIF SetImagingSettings carries CrGain (red) and CbGain (blue) channel
	// gains — they are independent values and must not be forced equal. Legacy
	// callers may have set only ColorTemperature; preserve that behavior by
	// using it for both gains when the explicit gains are unset.
	crGain, cbGain := wb.CrGain, wb.CbGain
	if crGain == 0 && cbGain == 0 {
		crGain, cbGain = wb.ColorTemperature, wb.ColorTemperature
	}
	return fmt.Sprintf(`<tt:WhiteBalance>
  <tt:Mode>%s</tt:Mode>
  <tt:CrGain>%f</tt:CrGain>
  <tt:CbGain>%f</tt:CbGain>
</tt:WhiteBalance>`, mode, crGain, cbGain)
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// Compile-time interface check.
var _ ImagingController = (*ImagingControllerImpl)(nil)
