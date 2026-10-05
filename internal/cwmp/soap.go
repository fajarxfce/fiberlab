// Package cwmp implements a bounded TR-069/CWMP 1.0 CPE management session.
// The reference parameter model is TR-098; it does not emulate vendor firmware.
package cwmp

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const namespace = "urn:dslforum-org:cwmp-1-0"
const root = "InternetGatewayDevice."
const management = root + "ManagementServer."
const wlan = root + "LANDevice.1.WLANConfiguration.1."
const wan = root + "WANDevice.1.WANConnectionDevice.1.WANPPPConnection.1."
const maxSOAP = 2 << 20

type message struct {
	XMLName xml.Name
	Inner   []byte `xml:",innerxml"`
}

type envelope struct {
	XMLName xml.Name `xml:"Envelope"`
	Header  struct {
		ID string `xml:"ID"`
	} `xml:"Header"`
	Body struct {
		Messages []message `xml:",any"`
	} `xml:"Body"`
}

func parseEnvelope(body []byte) (envelope, error) {
	var e envelope
	if len(body) > maxSOAP {
		return e, fmt.Errorf("ACS SOAP response exceeds 2 MiB")
	}
	if err := xml.Unmarshal(body, &e); err != nil {
		return e, fmt.Errorf("invalid ACS SOAP XML")
	}
	if e.XMLName.Space != "http://schemas.xmlsoap.org/soap/envelope/" || len(e.Body.Messages) != 1 || len(e.Header.ID) > 256 {
		return e, fmt.Errorf("expected one SOAP 1.1 RPC")
	}
	m := e.Body.Messages[0]
	if m.XMLName.Local != "Fault" && m.XMLName.Space != namespace {
		return e, fmt.Errorf("unsupported ACS CWMP namespace")
	}
	return e, nil
}

func escape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

func soap(id, content string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<soap-env:Envelope xmlns:soap-env="http://schemas.xmlsoap.org/soap/envelope/" xmlns:soap-enc="http://schemas.xmlsoap.org/soap/encoding/" xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:cwmp="` + namespace + `">` +
		`<soap-env:Header><cwmp:ID soap-env:mustUnderstand="1">` + escape(id) + `</cwmp:ID></soap-env:Header><soap-env:Body>` + content + `</soap-env:Body></soap-env:Envelope>`)
}

type parameter struct {
	Value    string
	Type     string
	Writable bool
}

type event struct{ Code, Key string }

func parameterList(values map[string]parameter) string {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	fmt.Fprintf(&b, `<ParameterList soap-enc:arrayType="cwmp:ParameterValueStruct[%d]">`, len(keys))
	for _, name := range keys {
		p := values[name]
		fmt.Fprintf(&b, `<ParameterValueStruct><Name>%s</Name><Value xsi:type="xsd:%s">%s</Value></ParameterValueStruct>`, escape(name), p.Type, escape(p.Value))
	}
	b.WriteString(`</ParameterList>`)
	return b.String()
}

func inform(id string, input Input, values map[string]parameter, events []event, retry int) []byte {
	var b strings.Builder
	b.WriteString(`<cwmp:Inform><DeviceId><Manufacturer>Fiberlab</Manufacturer><OUI>024654</OUI><ProductClass>FiberlabONU</ProductClass><SerialNumber>`)
	b.WriteString(escape(serial(input)))
	fmt.Fprintf(&b, `</SerialNumber></DeviceId><Event soap-enc:arrayType="cwmp:EventStruct[%d]">`, len(events))
	for _, e := range events {
		fmt.Fprintf(&b, `<EventStruct><EventCode>%s</EventCode><CommandKey>%s</CommandKey></EventStruct>`, escape(e.Code), escape(e.Key))
	}
	fmt.Fprintf(&b, `</Event><MaxEnvelopes>1</MaxEnvelopes><CurrentTime>%s</CurrentTime><RetryCount>%d</RetryCount>`, time.Now().UTC().Format(time.RFC3339), retry)
	names := []string{root + "DeviceSummary", root + "DeviceInfo.SpecVersion", root + "DeviceInfo.HardwareVersion", root + "DeviceInfo.SoftwareVersion", root + "DeviceInfo.ProvisioningCode", management + "ConnectionRequestURL", management + "ParameterKey", wan + "ExternalIPAddress"}
	selected := make(map[string]parameter, len(names))
	for _, name := range names {
		selected[name] = values[name]
	}
	b.WriteString(parameterList(selected))
	b.WriteString(`</cwmp:Inform>`)
	return soap(id, b.String())
}

type parameterFault struct {
	Name    string
	Code    int
	Message string
}

func fault(id string, code int, detail string, faults ...parameterFault) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, `<soap-env:Fault><faultcode>Client</faultcode><faultstring>CWMP fault</faultstring><detail><cwmp:Fault><FaultCode>%d</FaultCode><FaultString>%s</FaultString>`, code, escape(detail))
	for _, f := range faults {
		fmt.Fprintf(&b, `<SetParameterValuesFault><ParameterName>%s</ParameterName><FaultCode>%d</FaultCode><FaultString>%s</FaultString></SetParameterValuesFault>`, escape(f.Name), f.Code, escape(f.Message))
	}
	b.WriteString(`</cwmp:Fault></detail></soap-env:Fault>`)
	return soap(id, b.String())
}

func parseBool(s string) (bool, error) {
	switch strings.TrimSpace(s) {
	case "1", "true":
		return true, nil
	case "0", "false":
		return false, nil
	default:
		return false, fmt.Errorf("expected a boolean")
	}
}

func boolValue(b bool) string { return strconv.FormatBool(b) }

func selectParameters(values map[string]parameter, names []string) (map[string]parameter, string) {
	out := map[string]parameter{}
	if len(names) > 2048 {
		return nil, "too many parameter names"
	}
	for _, name := range names {
		if p, ok := values[name]; ok {
			out[name] = p
			continue
		}
		found := false
		if name == "" || strings.HasSuffix(name, ".") {
			for k, p := range values {
				if strings.HasPrefix(k, name) {
					out[k] = p
					found = true
				}
			}
		}
		if !found {
			return nil, name
		}
	}
	return out, ""
}

func parameterNames(values map[string]parameter, path string, next bool) (map[string]bool, bool) {
	out := map[string]bool{}
	if p, ok := values[path]; ok && !next {
		out[path] = p.Writable
		return out, true
	}
	if path != "" && !strings.HasSuffix(path, ".") {
		return nil, false
	}
	for k, p := range values {
		if !strings.HasPrefix(k, path) {
			continue
		}
		suffix := strings.TrimPrefix(k, path)
		parts := strings.Split(suffix, ".")
		limit := len(parts)
		if next {
			limit = 1
		}
		for i := 0; i < limit; i++ {
			name := path + strings.Join(parts[:i+1], ".")
			if i < len(parts)-1 {
				name += "."
				out[name] = false
			} else {
				out[name] = p.Writable
			}
		}
	}
	return out, len(out) != 0
}
