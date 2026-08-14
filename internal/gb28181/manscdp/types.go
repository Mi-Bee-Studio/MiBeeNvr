// Package manscdp implements the MANSCDP (Manufacturer and System Control
// Description Protocol) XML codec used by GB/T 28181 SIP MESSAGE bodies.
//
// Each message type is an XML document distinguished by the CmdType attribute:
// the platform sends Control messages (DeviceControl) while devices answer
// with Response (Catalog/DeviceInfo/DeviceStatus/RecordInfo) or Notify
// (Keepalive/Alarm) roots. Root elements follow GB/T 28181-2016 § 9.3.
package manscdp

import "encoding/xml"

// CmdType identifies a MANSCDP command (the CmdType XML attribute).
type CmdType string

const (
	CmdCatalog         CmdType = "Catalog"
	CmdKeepalive       CmdType = "Keepalive"
	CmdDeviceInfo      CmdType = "DeviceInfo"
	CmdDeviceStatus    CmdType = "DeviceStatus"
	CmdRecordInfo      CmdType = "RecordInfo"
	CmdRecordInfoQuery CmdType = "RecordInfoQuery"
	CmdDeviceControl   CmdType = "DeviceControl"
	CmdAlarm           CmdType = "Alarm"
)

// Catalog is a device's response to a platform Catalog query. It lists the
// device's channels (Item entries) wrapped in a DeviceList element.
type Catalog struct {
	XMLName  xml.Name `xml:"Response"`
	CmdType  CmdType  `xml:"CmdType,attr"`
	SN       int      `xml:"SN,attr"`
	DeviceID string   `xml:"DeviceID"`
	SumNum   int      `xml:"SumNum"`
	Item     []Item   `xml:"DeviceList>Item"`
}

// Item is a single channel entry in a Catalog response.
type Item struct {
	XMLName      xml.Name `xml:"Item"`
	DeviceID     string   `xml:"DeviceID"`
	Name         string   `xml:"Name"`
	ParentID     string   `xml:"ParentID"`
	Parental     int      `xml:"Parental"`
	Status       string   `xml:"Status"`
	Manufacturer string   `xml:"Manufacturer"`
	Model        string   `xml:"Model"`
	Owner        string   `xml:"Owner"`
	CivilCode    string   `xml:"CivilCode"`
	Address      string   `xml:"Address"`
	SafetyWay    int      `xml:"SafetyWay"`
	RegisterWay  int      `xml:"RegisterWay"`
	CertNum      string   `xml:"CertNum"`
	Certifiable  int      `xml:"Certifiable"`
	ErrCode      int      `xml:"ErrCode"`
	EndTime      string   `xml:"EndTime"`
	Secrecy      int      `xml:"Secrecy"`
	PTZType      int      `xml:"PTZType"`
}

// Keepalive is a device heartbeat sent periodically to the platform.
type Keepalive struct {
	XMLName  xml.Name `xml:"Notify"`
	CmdType  CmdType  `xml:"CmdType,attr"`
	SN       int      `xml:"SN,attr"`
	DeviceID string   `xml:"DeviceID"`
	Status   string   `xml:"Status"`
}

// DeviceInfo is a device's response to a platform DeviceInfo query.
type DeviceInfo struct {
	XMLName      xml.Name `xml:"Response"`
	CmdType      CmdType  `xml:"CmdType,attr"`
	SN           int      `xml:"SN,attr"`
	DeviceID     string   `xml:"DeviceID"`
	DeviceName   string   `xml:"DeviceName"`
	Manufacturer string   `xml:"Manufacturer"`
	Model        string   `xml:"Model"`
	Firmware     string   `xml:"Firmware"`
	Channel      int      `xml:"Channel"`
	Result       string   `xml:"Result"`
}

// DeviceStatus is a device's response to a platform DeviceStatus query.
type DeviceStatus struct {
	XMLName  xml.Name `xml:"Response"`
	CmdType  CmdType  `xml:"CmdType,attr"`
	SN       int      `xml:"SN,attr"`
	DeviceID string   `xml:"DeviceID"`
	Status   string   `xml:"Status"`
	Time     string   `xml:"Time"`
}

// RecordInfo is a device's response to a platform RecordInfo query listing
// its recorded segments within a requested time range.
type RecordInfo struct {
	XMLName    xml.Name     `xml:"Response"`
	CmdType    CmdType      `xml:"CmdType,attr"`
	SN         int          `xml:"SN,attr"`
	DeviceID   string       `xml:"DeviceID"`
	Name       string       `xml:"Name"`
	SumNum     int          `xml:"SumNum"`
	RecordList []RecordItem `xml:"RecordList>Item"`
}

// RecordItem is a single recorded segment in a RecordInfo response.
type RecordItem struct {
	XMLName    xml.Name `xml:"Item"`
	DeviceID   string   `xml:"DeviceID"`
	Name       string   `xml:"Name"`
	FilePath   string   `xml:"FilePath"`
	Address    string   `xml:"Address"`
	StartTime  string   `xml:"StartTime"`
	EndTime    string   `xml:"EndTime"`
	Secrecy    int      `xml:"Secrecy"`
	Type       string   `xml:"Type"`
	RecorderID string   `xml:"RecorderID"`
}

// DeviceControl is a platform-to-device Control message. PTZCmd carries the
// 8-byte GB/T 28181 § A.4 PTZ command (XML-escaped binary), and other
// controls (record, guard, alarm, home) are expressed via the respective
// optional fields.
type DeviceControl struct {
	XMLName      xml.Name `xml:"Control"`
	CmdType      CmdType  `xml:"CmdType,attr"`
	SN           int      `xml:"SN,attr"`
	DeviceID     string   `xml:"DeviceID"`
	PTZCmd       string   `xml:"PTZCmd"`
	HomePosition string   `xml:"HomePosition"`
	TeleCmd      string   `xml:"TeleCmd"`
}

// Alarm is a device-initiated alarm notification sent to the platform.
type Alarm struct {
	XMLName          xml.Name `xml:"Notify"`
	CmdType          CmdType  `xml:"CmdType,attr"`
	SN               int      `xml:"SN,attr"`
	DeviceID         string   `xml:"DeviceID"`
	AlarmPriority    string   `xml:"AlarmPriority"`
	AlarmMethod      string   `xml:"AlarmMethod"`
	AlarmTime        string   `xml:"AlarmTime"`
	AlarmDescription string   `xml:"AlarmDescription"`
	AlarmType        string   `xml:"AlarmType"`
}

// RecordInfoQuery is a platform-to-device request for recording information
// within a specific time range.
type RecordInfoQuery struct {
	XMLName   xml.Name `xml:"Query"`
	CmdType   CmdType  `xml:"CmdType,attr"`
	SN        int      `xml:"SN,attr"`
	DeviceID  string   `xml:"DeviceID"`
	StartTime string   `xml:"StartTime"`
	EndTime   string   `xml:"EndTime"`
	Type      string   `xml:"Type"`
}

// CatalogQuery is a platform-to-device request for the device's channel catalog.
// The device responds with a Catalog response listing its channels.
type CatalogQuery struct {
	XMLName  xml.Name `xml:"Query"`
	CmdType  CmdType  `xml:"CmdType,attr"`
	SN       int      `xml:"SN,attr"`
	DeviceID string   `xml:"DeviceID"`
}
