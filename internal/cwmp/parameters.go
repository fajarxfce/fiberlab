package cwmp

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"ftthlab/internal/model"
)

type Input struct {
	LabID, RunID string
	Node         model.Node
	Subscriber   model.Subscriber
	Session      model.Session
	NodeState    model.NodeState
	Config       model.ACSConfig
}

type persistentState struct {
	Parameters   map[string]string `json:"parameters"`
	Bootstrapped bool              `json:"bootstrapped"`
	SettingsKey  string            `json:"settingsKey"`
	ParameterKey string            `json:"parameterKey"`
}

func serial(in Input) string {
	return in.Node.Config.Serial + "-" + strings.TrimPrefix(in.LabID, "lab-")
}

// GenieACS escapes hyphens inside each identity component so its separators
// remain unambiguous. The SOAP SerialNumber itself stays unchanged.
func deviceID(in Input) string {
	return "024654-FiberlabONU-" + strings.ReplaceAll(url.PathEscape(serial(in)), "-", "%2D")
}
func callbackURL(in Input) string {
	return strings.TrimRight(in.Config.ConnectionRequestURL, "/") + "/cwmp/" + url.PathEscape(in.LabID) + "/" + url.PathEscape(in.Node.ID)
}

func (c *cpe) value(name, fallback string) string {
	if v, ok := c.state.Parameters[name]; ok {
		return v
	}
	return fallback
}

// values is called with c.mu held. Password parameters are write-only as
// required by TR-098; credentials never appear in GetParameterValues or Inform.
func (c *cpe) values() map[string]parameter {
	in := c.input
	out := map[string]parameter{}
	add := func(name, typ, value string, writable bool) {
		out[name] = parameter{Value: value, Type: typ, Writable: writable}
	}
	str := func(name, value string) { add(name, "string", value, false) }
	uint := func(name string, value uint64) {
		add(name, "unsignedInt", strconv.FormatUint(value&0xffffffff, 10), false)
	}
	writable := func(name, typ, fallback string) { add(name, typ, c.value(name, fallback), true) }
	str(root+"DeviceSummary", "InternetGatewayDevice:1.4[](Baseline:1)")
	str(root+"DeviceInfo.Manufacturer", "Fiberlab")
	str(root+"DeviceInfo.ManufacturerOUI", "024654")
	str(root+"DeviceInfo.ModelName", "Fiberlab ONU")
	str(root+"DeviceInfo.ProductClass", "FiberlabONU")
	str(root+"DeviceInfo.SerialNumber", serial(in))
	str(root+"DeviceInfo.SpecVersion", "1.0")
	str(root+"DeviceInfo.HardwareVersion", "GPON reference")
	str(root+"DeviceInfo.SoftwareVersion", "Fiberlab-0.2.0")
	writable(root+"DeviceInfo.ProvisioningCode", "string", "")
	uint(root+"DeviceInfo.UpTime", uint64(time.Since(c.born).Seconds()))
	writable(management+"URL", "string", in.Config.URL)
	writable(management+"Username", "string", in.Config.Username)
	add(management+"Password", "string", "", true)
	writable(management+"PeriodicInformEnable", "boolean", "true")
	writable(management+"PeriodicInformInterval", "unsignedInt", strconv.Itoa(in.Config.PeriodicInformSeconds))
	str(management+"ParameterKey", c.state.ParameterKey)
	str(management+"ConnectionRequestURL", callbackURL(in))
	writable(management+"ConnectionRequestUsername", "string", in.Config.ConnectionRequestUsername)
	add(management+"ConnectionRequestPassword", "string", "", true)
	uint(root+"LANDeviceNumberOfEntries", 1)
	uint(root+"WANDeviceNumberOfEntries", 1)
	uint(root+"LANDevice.1.WLANConfigurationNumberOfEntries", 1)
	writable(wlan+"Enable", "boolean", "true")
	writable(wlan+"SSID", "string", "Fiberlab-"+in.Node.Config.Serial)
	add(wlan+"KeyPassphrase", "string", "", true)
	add(wlan+"PreSharedKey.1.KeyPassphrase", "string", "", true)
	str(wlan+"BSSID", in.Node.Config.MAC)
	if c.value(wlan+"Enable", "true") == "true" {
		str(wlan+"Status", "Up")
	} else {
		str(wlan+"Status", "Disabled")
	}
	str(wlan+"BeaconType", "11i")
	str(wlan+"IEEE11iAuthenticationMode", "PSKAuthentication")
	str(wlan+"IEEE11iEncryptionModes", "AESEncryption")
	uint(root+"WANDevice.1.WANConnectionDeviceNumberOfEntries", 1)
	uint(root+"WANDevice.1.WANConnectionDevice.1.WANPPPConnectionNumberOfEntries", 1)
	add(wan+"Enable", "boolean", boolValue(in.Node.Config.AdminUp), false)
	str(wan+"Name", "Fiberlab PPPoE")
	str(wan+"ConnectionType", "IP_Routed")
	str(wan+"Username", in.Subscriber.Username)
	str(wan+"Password", "")
	str(wan+"ExternalIPAddress", in.Session.Address)
	if in.Session.Status == "connected" {
		str(wan+"ConnectionStatus", "Connected")
	} else {
		str(wan+"ConnectionStatus", "Disconnected")
	}
	uint(wan+"Uptime", uint64(max(0, in.Session.UptimeSeconds)))
	uint(wan+"Stats.EthernetBytesReceived", in.Session.RXBytes)
	uint(wan+"Stats.EthernetBytesSent", in.Session.TXBytes)
	str(root+"X_FIBERLAB_GPONSerial", in.Node.Config.Serial)
	str(root+"X_FIBERLAB_OLT", in.NodeState.OLTID)
	str(root+"X_FIBERLAB_PON", in.NodeState.PON)
	if in.NodeState.RXDBm != nil {
		str(root+"X_FIBERLAB_RXPower", strconv.FormatFloat(*in.NodeState.RXDBm, 'f', 2, 64))
	} else {
		str(root+"X_FIBERLAB_RXPower", "")
	}
	return out
}

func validText(s string, maximum int) bool {
	return len(s) <= maximum && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

func validateValue(name, typ, value string) (string, int, string) {
	if !validText(value, 2048) {
		return "", 9007, "Value is too long or contains control characters"
	}
	switch name {
	case management + "URL":
		if err := model.ValidateACSURL(value); err != nil {
			return "", 9007, err.Error()
		}
	case management + "Username", management + "Password":
		if len(value) > 256 {
			return "", 9007, "Credential exceeds 256 characters"
		}
	case management + "ConnectionRequestUsername":
		if len(value) == 0 || len(value) > 256 {
			return "", 9007, "Connection-request username must have 1–256 characters"
		}
	case management + "ConnectionRequestPassword":
		if len(value) < 8 || len(value) > 256 {
			return "", 9007, "Connection-request password must have 8–256 characters"
		}
	case management + "PeriodicInformInterval":
		n, err := strconv.Atoi(value)
		if err != nil || n < 10 || n > 86400 {
			return "", 9007, "Reference profile supports 10–86400 seconds"
		}
		value = strconv.Itoa(n)
	case wlan + "SSID":
		if len(value) < 1 || len(value) > 32 {
			return "", 9007, "SSID must have 1–32 bytes"
		}
	case wlan + "KeyPassphrase", wlan + "PreSharedKey.1.KeyPassphrase":
		if len(value) < 8 || len(value) > 63 {
			return "", 9007, "WPA passphrase must have 8–63 characters"
		}
	case root + "DeviceInfo.ProvisioningCode":
		if len(value) > 64 {
			return "", 9007, "Provisioning code exceeds 64 characters"
		}
	}
	if typ == "boolean" {
		b, err := parseBool(value)
		if err != nil {
			return "", 9007, "Expected true, false, 1 or 0"
		}
		value = boolValue(b)
	}
	return value, 0, ""
}

type rpcAction struct{ Kind, Key string }

func (c *cpe) handleRPC(ctx context.Context, e envelope, hooks Hooks) ([]byte, *rpcAction) {
	m := e.Body.Messages[0]
	id := e.Header.ID
	decode := func(v any) error {
		return xml.Unmarshal(append(append([]byte("<rpc>"), m.Inner...), []byte("</rpc>")...), v)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	values := c.values()
	switch m.XMLName.Local {
	case "GetRPCMethods":
		methods := []string{"GetRPCMethods", "GetParameterNames", "GetParameterValues", "SetParameterValues", "Reboot", "FactoryReset"}
		var b strings.Builder
		fmt.Fprintf(&b, `<cwmp:GetRPCMethodsResponse><MethodList soap-enc:arrayType="xsd:string[%d]">`, len(methods))
		for _, method := range methods {
			b.WriteString("<string>" + method + "</string>")
		}
		b.WriteString(`</MethodList></cwmp:GetRPCMethodsResponse>`)
		return soap(id, b.String()), nil
	case "GetParameterValues":
		var args struct {
			Names []string `xml:"ParameterNames>string"`
		}
		if decode(&args) != nil {
			return fault(id, 9003, "Invalid GetParameterValues arguments"), nil
		}
		selected, missing := selectParameters(values, args.Names)
		if missing != "" {
			return fault(id, 9005, "Unknown parameter: "+missing), nil
		}
		return soap(id, `<cwmp:GetParameterValuesResponse>`+parameterList(selected)+`</cwmp:GetParameterValuesResponse>`), nil
	case "GetParameterNames":
		var args struct {
			Path string `xml:"ParameterPath"`
			Next string `xml:"NextLevel"`
		}
		if decode(&args) != nil {
			return fault(id, 9003, "Invalid GetParameterNames arguments"), nil
		}
		next, err := parseBool(args.Next)
		if err != nil || (next && args.Path != "" && !strings.HasSuffix(args.Path, ".")) {
			return fault(id, 9003, "NextLevel requires an object path"), nil
		}
		names, ok := parameterNames(values, args.Path, next)
		if !ok {
			return fault(id, 9005, "Unknown parameter path"), nil
		}
		keys := make([]string, 0, len(names))
		for name := range names {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		var b strings.Builder
		fmt.Fprintf(&b, `<cwmp:GetParameterNamesResponse><ParameterList soap-enc:arrayType="cwmp:ParameterInfoStruct[%d]">`, len(keys))
		for _, name := range keys {
			fmt.Fprintf(&b, `<ParameterInfoStruct><Name>%s</Name><Writable>%s</Writable></ParameterInfoStruct>`, escape(name), boolValue(names[name]))
		}
		b.WriteString(`</ParameterList></cwmp:GetParameterNamesResponse>`)
		return soap(id, b.String()), nil
	case "SetParameterValues":
		var args struct {
			Parameters []struct {
				Name  string `xml:"Name"`
				Value struct {
					Type string `xml:"type,attr"`
					Text string `xml:",chardata"`
				} `xml:"Value"`
			} `xml:"ParameterList>ParameterValueStruct"`
			Key string `xml:"ParameterKey"`
		}
		if decode(&args) != nil || len(args.Parameters) > 256 || !validText(args.Key, 32) {
			return fault(id, 9003, "Invalid SetParameterValues arguments"), nil
		}
		next := c.state
		next.Parameters = make(map[string]string, len(c.state.Parameters)+len(args.Parameters))
		for k, v := range c.state.Parameters {
			next.Parameters[k] = v
		}
		seen := map[string]bool{}
		faults := []parameterFault{}
		for _, change := range args.Parameters {
			name := change.Name
			if seen[name] {
				return fault(id, 9003, "Duplicate parameter names"), nil
			}
			seen[name] = true
			p, ok := values[name]
			if !ok {
				faults = append(faults, parameterFault{name, 9005, "Unknown parameter"})
				continue
			}
			if !p.Writable {
				faults = append(faults, parameterFault{name, 9008, "Parameter is read-only"})
				continue
			}
			typ := change.Value.Type
			if i := strings.LastIndex(typ, ":"); i >= 0 {
				typ = typ[i+1:]
			}
			if typ != p.Type {
				faults = append(faults, parameterFault{name, 9006, "Incorrect parameter type"})
				continue
			}
			value, code, message := validateValue(name, typ, change.Value.Text)
			if code != 0 {
				faults = append(faults, parameterFault{name, code, message})
				continue
			}
			next.Parameters[name] = value
			if name == wlan+"KeyPassphrase" || name == wlan+"PreSharedKey.1.KeyPassphrase" {
				next.Parameters[wlan+"KeyPassphrase"] = value
				next.Parameters[wlan+"PreSharedKey.1.KeyPassphrase"] = value
			}
		}
		if len(faults) > 0 {
			return fault(id, 9003, "One or more parameter values are invalid", faults...), nil
		}
		next.ParameterKey = args.Key
		body, _ := json.Marshal(next)
		if err := hooks.Save(ctx, c.input.LabID, c.input.Node.ID, body); err != nil {
			return fault(id, 9002, "Could not persist parameter values"), nil
		}
		c.state = next
		return soap(id, `<cwmp:SetParameterValuesResponse><Status>0</Status></cwmp:SetParameterValuesResponse>`), nil
	case "Reboot", "FactoryReset":
		var args struct {
			Key string `xml:"CommandKey"`
		}
		if decode(&args) != nil || !validText(args.Key, 32) {
			return fault(id, 9003, "Invalid command key"), nil
		}
		if hooks.Reboot == nil {
			return fault(id, 9001, "ONU reboot is unavailable"), nil
		}
		if m.XMLName.Local == "FactoryReset" {
			next := persistentState{Parameters: map[string]string{}, SettingsKey: c.state.SettingsKey}
			body, _ := json.Marshal(next)
			if err := hooks.Save(ctx, c.input.LabID, c.input.Node.ID, body); err != nil {
				return fault(id, 9002, "Could not persist ONU factory reset"), nil
			}
			c.state = next
		}
		return soap(id, `<cwmp:`+m.XMLName.Local+`Response/>`), &rpcAction{Kind: m.XMLName.Local, Key: args.Key}
	default:
		return fault(id, 9000, "RPC is not supported by the Fiberlab reference profile"), nil
	}
}
