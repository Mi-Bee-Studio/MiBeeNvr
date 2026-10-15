package dlna

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
)

// UDN derives the device UUID from a stable machine key (hostname + storage
// root): DLNA control points key everything off the UDN, so it must survive
// restarts without persisting state.
func UDN(machineKey string) string {
	sum := sha256.Sum256([]byte("mibee-nvr-dlna:" + machineKey))
	h := hex.EncodeToString(sum[:16])
	return "uuid:" + h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// deviceDescription renders the UPnP device description document. urlBase is
// the per-interface base URL (http://<iface-ip>:<port>) so relative service
// URLs resolve for the interface the control point discovered us on.
func deviceDescription(friendlyName, udn, urlBase string) []byte {
	var b bytes.Buffer
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<root xmlns="urn:schemas-upnp-org:device-1-0">` + "\n")
	b.WriteString("  <specVersion><major>1</major><minor>0</minor></specVersion>\n")
	b.WriteString("  <URLBase>" + xmlEscape(urlBase) + "</URLBase>\n")
	b.WriteString("  <device>\n")
	b.WriteString("    <deviceType>urn:schemas-upnp-org:device:MediaServer:1</deviceType>\n")
	b.WriteString("    <friendlyName>" + xmlEscape(friendlyName) + "</friendlyName>\n")
	b.WriteString("    <manufacturer>MiBee Studio</manufacturer>\n")
	b.WriteString("    <manufacturerURL>https://github.com/Mi-Bee-Studio/MiBeeNvr</manufacturerURL>\n")
	b.WriteString("    <modelDescription>MiBee NVR — cameras and recordings</modelDescription>\n")
	b.WriteString("    <modelName>MiBee NVR</modelName>\n")
	b.WriteString("    <modelNumber>1</modelNumber>\n")
	b.WriteString("    <UDN>" + xmlEscape(udn) + "</UDN>\n")
	b.WriteString("    <serviceList>\n")
	writeService(&b, "ContentDirectory", "urn:schemas-upnp-org:service:ContentDirectory:1")
	writeService(&b, "ConnectionManager", "urn:schemas-upnp-org:service:ConnectionManager:1")
	b.WriteString("    </serviceList>\n")
	b.WriteString("  </device>\n")
	b.WriteString("</root>\n")
	return b.Bytes()
}

func writeService(b *bytes.Buffer, name, serviceType string) {
	b.WriteString("      <service>\n")
	b.WriteString("        <serviceType>" + serviceType + "</serviceType>\n")
	b.WriteString("        <serviceId>urn:upnp-org:serviceId:" + name + "</serviceId>\n")
	b.WriteString("        <SCPDURL>/dlna/" + name + "/scpd.xml</SCPDURL>\n")
	b.WriteString("        <controlURL>/dlna/" + name + "/control</controlURL>\n")
	b.WriteString("        <eventSubURL>/dlna/" + name + "/event</eventSubURL>\n")
	b.WriteString("      </service>\n")
}

// scpdContentDirectory is the minimal standard SCPD for ContentDirectory:1
// with the Browse action (Search and the X_* extensions are not implemented
// and not advertised).
func scpdContentDirectory() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<scpd xmlns="urn:schemas-upnp-org:service-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <actionList>
    <action>
      <name>Browse</name>
      <argumentList>
        <argument><name>ObjectID</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_ObjectID</relatedStateVariable></argument>
        <argument><name>BrowseFlag</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_BrowseFlag</relatedStateVariable></argument>
        <argument><name>Filter</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Filter</relatedStateVariable></argument>
        <argument><name>StartingIndex</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Index</relatedStateVariable></argument>
        <argument><name>RequestedCount</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>
        <argument><name>SortCriteria</name><direction>in</direction><relatedStateVariable>A_ARG_TYPE_SortCriteria</relatedStateVariable></argument>
        <argument><name>Result</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Result</relatedStateVariable></argument>
        <argument><name>NumberReturned</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>
        <argument><name>TotalMatches</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_Count</relatedStateVariable></argument>
        <argument><name>UpdateID</name><direction>out</direction><relatedStateVariable>A_ARG_TYPE_UpdateID</relatedStateVariable></argument>
      </argumentList>
    </action>
  </actionList>
  <serviceStateTable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_ObjectID</name><dataType>string</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_BrowseFlag</name><dataType>string</dataType><allowedValueList><allowedValue>BrowseMetadata</allowedValue><allowedValue>BrowseDirectChildren</allowedValue></allowedValueList></stateVariable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_Filter</name><dataType>string</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_Index</name><dataType>ui4</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_Count</name><dataType>ui4</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_SortCriteria</name><dataType>string</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_Result</name><dataType>string</dataType></stateVariable>
    <stateVariable sendEvents="no"><name>A_ARG_TYPE_UpdateID</name><dataType>ui4</dataType></stateVariable>
    <stateVariable sendEvents="yes"><name>SystemUpdateID</name><dataType>ui4</dataType></stateVariable>
    <stateVariable sendEvents="yes"><name>ContainerUpdateIDs</name><dataType>string</dataType></stateVariable>
  </serviceStateTable>
</scpd>
`)
}

// scpdConnectionManager is the minimal standard SCPD for ConnectionManager:1
// — GetProtocolInfo and GetCurrentConnectionIDs with zero connections.
func scpdConnectionManager() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>
<scpd xmlns="urn:schemas-upnp-org:service-1-0">
  <specVersion><major>1</major><minor>0</minor></specVersion>
  <actionList>
    <action>
      <name>GetProtocolInfo</name>
      <argumentList>
        <argument><name>Source</name><direction>out</direction><relatedStateVariable>SourceProtocolInfo</relatedStateVariable></argument>
        <argument><name>Sink</name><direction>out</direction><relatedStateVariable>SinkProtocolInfo</relatedStateVariable></argument>
      </argumentList>
    </action>
    <action>
      <name>GetCurrentConnectionIDs</name>
      <argumentList>
        <argument><name>ConnectionIDs</name><direction>out</direction><relatedStateVariable>CurrentConnectionIDs</relatedStateVariable></argument>
      </argumentList>
    </action>
  </actionList>
  <serviceStateTable>
    <stateVariable sendEvents="yes"><name>SourceProtocolInfo</name><dataType>string</dataType></stateVariable>
    <stateVariable sendEvents="yes"><name>SinkProtocolInfo</name><dataType>string</dataType></stateVariable>
    <stateVariable sendEvents="yes"><name>CurrentConnectionIDs</name><dataType>string</dataType></stateVariable>
  </serviceStateTable>
</scpd>
`)
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// fmtDuration renders seconds in the UPnP duration form H:MM:SS (fractional
// part truncated — the value is a seek hint, not a contract).
func fmtDuration(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	total := int(sec)
	h := total / 3600
	m := (total % 3600) / 60
	s := total % 60
	return fmt.Sprintf("%d:%02d:%02d", h, m, s)
}
