package dlna

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// UPnP error codes used by this server.
const (
	errInvalidAction = 401
	errNoSuchObjectC = 701
)

// browseArgs carries the parsed CDS Browse arguments.
type browseArgs struct {
	ObjectID       string
	BrowseFlag     string
	StartingIndex  int
	RequestedCount int
}

// parseBrowse extracts Browse arguments from a SOAP body by walking element
// local names — SSDP/SOAP stacks disagree on namespace prefixes and the
// UPnP spec allows both qualified and unqualified children, so structural
// matching beats schema decoding.
func parseBrowse(body []byte) (browseArgs, error) {
	var args browseArgs
	dec := xml.NewDecoder(bytes.NewReader(body))
	inBrowse := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return args, fmt.Errorf("soap body: %w", err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "Browse":
				inBrowse = true
			case "ObjectID", "BrowseFlag", "StartingIndex", "RequestedCount":
				if !inBrowse {
					continue
				}
				text, err := elementText(dec)
				if err != nil {
					return args, err
				}
				switch el.Name.Local {
				case "ObjectID":
					args.ObjectID = text
				case "BrowseFlag":
					args.BrowseFlag = text
				case "StartingIndex":
					args.StartingIndex, _ = strconv.Atoi(text)
				case "RequestedCount":
					args.RequestedCount, _ = strconv.Atoi(text)
				}
			}
		case xml.EndElement:
			if el.Name.Local == "Browse" {
				inBrowse = false
			}
		}
	}
	if args.ObjectID == "" && args.BrowseFlag == "" {
		return args, fmt.Errorf("no Browse action in body")
	}
	return args, nil
}

// elementText reads the character data of the current element.
func elementText(dec *xml.Decoder) (string, error) {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", err
		}
		switch el := tok.(type) {
		case xml.CharData:
			sb.Write(el)
		case xml.EndElement:
			return sb.String(), nil
		case xml.StartElement:
			// nested element (shouldn't happen) — skip it
			if err := dec.Skip(); err != nil {
				return "", err
			}
		}
	}
}

// soapActionName extracts the action from the SOAPAction header, e.g.
// "urn:schemas-upnp-org:service:ContentDirectory:1#Browse" → "Browse".
func soapActionName(header string) string {
	h := header
	if i := bytes.IndexByte([]byte(h), '#'); i >= 0 {
		h = h[i+1:]
	}
	return strings.Trim(h, `"`)
}

// soapResponse wraps inner response elements in the UPnP SOAP envelope.
func soapResponse(serviceType, action, inner string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">`)
	b.WriteString(`<s:Body><u:` + action + `Response xmlns:u="` + serviceType + `">`)
	b.WriteString(inner)
	b.WriteString(`</u:` + action + `Response></s:Body></s:Envelope>`)
	return b.Bytes()
}

// soapFault renders a UPnP error response.
func soapFault(code int, description string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">`)
	b.WriteString(`<s:Body><s:Fault><faultcode>s:Client</faultcode><faultstring>UPnPError</faultstring>`)
	b.WriteString(`<detail><UPnPError xmlns="urn:schemas-upnp-org:control-1-0"><errorCode>` + strconv.Itoa(code) + `</errorCode><errorDescription>` + xmlEscape(description) + `</errorDescription></UPnPError></detail>`)
	b.WriteString(`</s:Fault></s:Body></s:Envelope>`)
	return b.Bytes()
}

// paginate applies CDS StartingIndex/RequestedCount to a browse result's
// fragment list semantics: the DIDL string is already built, so pagination
// slices at the fragment level before document assembly via the caller.
// Implemented here as integer window math shared by the handler.
func paginate(total, start, count int) (from, to int) {
	if start < 0 {
		start = 0
	}
	if start >= total {
		return total, total
	}
	from = start
	to = total
	if count > 0 && start+count < total {
		to = start + count
	}
	return from, to
}
