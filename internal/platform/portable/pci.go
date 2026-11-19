package portable

import "strings"

var pciClasses = map[string]string{
	"0100": "SCSI storage controller", "0101": "IDE interface", "0104": "RAID bus controller", "0106": "SATA controller",
	"0107": "Serial Attached SCSI controller", "0108": "Non-Volatile memory controller", "0180": "Mass storage controller",
	"0200": "Ethernet controller", "0280": "Network controller", "0300": "VGA compatible controller", "0302": "3D controller",
	"0380": "Display controller", "0401": "Multimedia audio controller", "0403": "Audio device", "0480": "Multimedia controller",
	"0500": "RAM memory", "0580": "Memory controller", "0600": "Host bridge", "0601": "ISA bridge", "0604": "PCI bridge",
	"0680": "Bridge", "0780": "Communication controller", "0800": "PIC", "0880": "System peripheral", "0c03": "USB controller",
	"0c05": "SMBus", "0c80": "Serial bus controller", "0d80": "Wireless controller", "1080": "Encryption controller",
	"1180": "Signal processing controller", "1200": "Processing accelerators", "1300": "Non-Essential Instrumentation",
}

var pciBaseClasses = map[string]string{
	"01": "Mass storage controller", "02": "Network controller", "03": "Display controller", "04": "Multimedia controller",
	"05": "Memory controller", "06": "Bridge", "07": "Communication controller", "08": "System peripheral",
	"09": "Input device controller", "0c": "Serial bus controller", "0d": "Wireless controller", "10": "Encryption controller",
	"11": "Signal processing controller", "12": "Processing accelerators",
}

// PCIClassName names a PCI class code such as "0x030000", or returns
// "Unknown".
func PCIClassName(code string) string {
	c := strings.TrimPrefix(strings.ToLower(code), "0x")
	if len(c) < 4 {
		return "Unknown"
	}
	if n, ok := pciClasses[c[:4]]; ok {
		return n
	}
	if n, ok := pciBaseClasses[c[:2]]; ok {
		return n
	}
	return "Unknown"
}
