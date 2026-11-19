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

// pciVendors names common PCI vendors, for systems without a pci.ids
// database (macOS, Windows). Windows' own "manufacturer" is the driver's
// vendor ("Standard NVM Express Controller"), not the hardware's.
var pciVendors = map[uint16]string{
	0x106b: "Apple", 0x8086: "Intel", 0x8087: "Intel", 0x1002: "AMD", 0x1022: "AMD", 0x10de: "NVIDIA",
	0x14e4: "Broadcom", 0x144d: "Samsung", 0x1b4b: "Marvell", 0x1d6a: "Aquantia", 0x1af4: "Red Hat (virtio)",
	0x15ad: "VMware", 0x1b21: "ASMedia", 0x1912: "Renesas", 0x12d8: "Pericom", 0x10ec: "Realtek",
	0x168c: "Qualcomm Atheros", 0x17cb: "Qualcomm", 0x1987: "Phison", 0x15b7: "Western Digital", 0x1c5c: "SK hynix",
	0x126f: "Silicon Motion", 0x1cc1: "ADATA", 0x2646: "Kingston", 0xc0a9: "Micron", 0x1344: "Micron",
	0x1414: "Microsoft", 0x1d0f: "Amazon", 0x1ae0: "Google", 0x1b36: "Red Hat (QEMU)", 0x80ee: "VirtualBox",
	0x1106: "VIA", 0x1033: "NEC", 0x197b: "JMicron", 0x1217: "O2 Micro", 0x10b5: "PLX", 0x1000: "Broadcom (LSI)",
	0x9005: "Microchip (Adaptec)", 0x15b3: "Mellanox", 0x8088: "Wangxun", 0x1425: "Chelsio", 0x177d: "Cavium",
}

// PCIVendorName names a PCI vendor ID, or returns "".
func PCIVendorName(id uint16) string { return pciVendors[id] }
