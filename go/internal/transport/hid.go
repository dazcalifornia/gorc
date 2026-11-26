package transport

// KindUSBHID is a USB Human Interface Device that is not a keyboard or mouse:
// some clinical thermometers present themselves this way instead of as a
// serial port. Each Frame is one HID input report, report ID byte included.
const KindUSBHID Kind = "usb-hid"

// USBHID is the HID transport. The platform files implement it; only Windows
// is supported, which is where the OPD kiosks that use these devices run.
type USBHID struct{}

// NewUSBHID returns the USB-HID transport.
func NewUSBHID() *USBHID { return &USBHID{} }

func (h *USBHID) Kind() Kind { return KindUSBHID }

// hidSuggest maps a VID:PID to the decoder profile that understands it.
var hidSuggest = map[string]string{
	"0C45:7702": "thermo-hid-0c45", // SMART EASY OPD's USB thermometer (GenericHid.dll)
}
