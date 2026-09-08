// SPDX-License-Identifier: MIT

package onvif

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ONVIF service namespaces whose SOAP actions must be routed away from the
// device service endpoint (#723): trt:* (media) belongs at the media XAddr the
// device advertises. Minimal implementations (ESP32 MiBeeCam firmware) answer
// "Unsupported device action" when trt:* lands on device_service, so raw-SOAP
// media calls must follow the advertisement instead of relying on fallbacks.
const (
	nsMediaVer10 = "http://www.onvif.org/ver10/media/wsdl"
	nsMediaVer20 = "http://www.onvif.org/ver20/media/wsdl"
)

const soapGetServices = `<?xml version="1.0" encoding="UTF-8"?>
<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"
 xmlns:tds="http://www.onvif.org/ver10/device/wsdl">
  <s:Body>
    <tds:GetServices>
      <tds:IncludeCapability>false</tds:IncludeCapability>
    </tds:GetServices>
  </s:Body>
</s:Envelope>`

// fetchServiceXAddrs asks the device service which endpoints host which
// namespaces and returns a namespace → XAddr map.
func fetchServiceXAddrs(ctx context.Context, deviceEndpoint string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceEndpoint, strings.NewReader(soapGetServices))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/soap+xml")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var envelope struct {
		Body struct {
			GetServicesResponse struct {
				Service []struct {
					Namespace string `xml:"Namespace"`
					XAddr     string `xml:"XAddr"`
				} `xml:"Service"`
			} `xml:"GetServicesResponse"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	xaddrs := make(map[string]string, len(envelope.Body.GetServicesResponse.Service))
	for _, svc := range envelope.Body.GetServicesResponse.Service {
		if svc.Namespace != "" && svc.XAddr != "" {
			xaddrs[svc.Namespace] = svc.XAddr
		}
	}
	if len(xaddrs) == 0 {
		return nil, fmt.Errorf("no services advertised")
	}
	return xaddrs, nil
}

// resolveMediaEndpoint returns the advertised media-service XAddr, rewritten
// to the device endpoint's host when the advertisement is stale (camera roamed
// to a new address; mirrors the library's fixServiceURL). Empty string means
// nothing usable was advertised — callers keep the device endpoint.
func resolveMediaEndpoint(ctx context.Context, deviceEndpoint string) (string, error) {
	xaddrs, err := fetchServiceXAddrs(ctx, deviceEndpoint)
	if err != nil {
		return "", err
	}
	advertised := xaddrs[nsMediaVer10]
	if advertised == "" {
		advertised = xaddrs[nsMediaVer20]
	}
	if advertised == "" {
		return "", nil
	}
	return rewriteStaleHost(advertised, deviceEndpoint), nil
}

// rewriteStaleHost swaps the XAddr's host for the device endpoint's host when
// the two disagree, keeping the advertised scheme and path.
func rewriteStaleHost(xaddr, deviceEndpoint string) string {
	xu, err := url.Parse(xaddr)
	if err != nil || xu.Host == "" {
		return xaddr
	}
	du, err := url.Parse(deviceEndpoint)
	if err != nil || du.Host == "" || du.Host == xu.Host {
		return xaddr
	}
	xu.Host = du.Host
	return xu.String()
}
