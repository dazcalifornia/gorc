package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func btClassicDescribe() Availability {
	return Availability{
		Kind:      KindBTClassic,
		Label:     "Bluetooth classic (SPP)",
		Available: true,
		Note: "macOS has no user-space RFCOMM API, so a classic device must first be paired in System Settings → Bluetooth. " +
			"Once paired, macOS publishes its Serial Port Profile as /dev/cu.<Name>-<Service> and this transport opens that port. " +
			"Paired devices with no SPP port are listed but cannot be opened.",
	}
}

func btClassicScan(ctx context.Context, s *Serial) ([]DeviceInfo, error) {
	// Ports first: these are the devices we can actually open.
	devs, err := s.Scan(ctx)
	if err != nil {
		return nil, err
	}
	byName := map[string]bool{}
	for i := range devs {
		devs[i].Details["link"] = "SPP serial port"
		byName[strings.ToLower(devs[i].Name)] = true
	}

	// Then paired devices, so a user whose oximeter is paired but has no SPP
	// port sees it in the list with an explanation instead of an empty panel.
	for _, p := range pairedClassicDevices(ctx) {
		if byName[strings.ToLower(p.Name)] {
			continue
		}
		devs = append(devs, p)
	}
	return devs, nil
}

func btClassicOpen(ctx context.Context, s *Serial, dev DeviceInfo, opts OpenOptions) (Conn, error) {
	if dev.Address == "" || !strings.HasPrefix(dev.Address, "/dev/") {
		return nil, fmt.Errorf("%q is paired but exposes no Serial Port Profile port on macOS; "+
			"pair a device that advertises SPP, or connect it over USB or BLE instead", dev.Name)
	}
	return s.Open(ctx, dev, opts)
}

// spBluetooth mirrors the shape of `system_profiler SPBluetoothDataType -json`.
// Apple nests the paired list differently across releases, so we accept the
// generic form and walk it.
type spBluetooth struct {
	Data []map[string]json.RawMessage `json:"SPBluetoothDataType"`
}

// pairedClassicDevices asks system_profiler which classic devices are paired.
// Failures are not fatal: the port list above is the part that matters.
func pairedClassicDevices(ctx context.Context) []DeviceInfo {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "system_profiler", "-json", "SPBluetoothDataType").Output()
	if err != nil {
		return nil
	}
	var root spBluetooth
	if json.Unmarshal(out, &root) != nil {
		return nil
	}

	var devs []DeviceInfo
	for _, section := range root.Data {
		for key, raw := range section {
			if !strings.Contains(key, "device") {
				continue
			}
			// The value is a list of single-key objects: {"Name": {...props}}.
			var entries []map[string]map[string]any
			if json.Unmarshal(raw, &entries) != nil {
				continue
			}
			for _, entry := range entries {
				for name, props := range entry {
					devs = append(devs, classicFromProps(name, props))
				}
			}
		}
	}
	return devs
}

func classicFromProps(name string, props map[string]any) DeviceInfo {
	str := func(k string) string {
		if v, ok := props[k].(string); ok {
			return v
		}
		return ""
	}
	addr := str("device_address")
	details := map[string]string{"link": "paired, no SPP port"}
	for label, key := range map[string]string{
		"address":   "device_address",
		"vendor":    "device_vendorID",
		"product":   "device_productID",
		"minorType": "device_minorType",
		"connected": "device_isconnected",
		"firmware":  "device_firmwareVersion",
	} {
		if v := str(key); v != "" {
			details[label] = v
		}
	}
	id := "bt-classic:paired:" + addr
	if addr == "" {
		id = "bt-classic:paired:" + name
	}
	return DeviceInfo{
		ID:      id,
		Kind:    KindBTClassic,
		Name:    name,
		Address: "", // not openable: no port
		Details: details,
	}
}
