//go:build linux

package linux

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// ---- filesystems ----

// statfsFunc returns capacity for a mount point. Injectable for tests.
type statfsFunc func(path string) (fsStats, error)

type fsStats struct {
	total, free, avail, files, filesFree uint64
	fsid                                 [2]int32
}

func realStatfs(path string) (fsStats, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(path, &s); err != nil {
		return fsStats{}, err
	}
	bs := uint64(s.Bsize) //nolint:gosec // block size is positive
	return fsStats{total: s.Blocks * bs, free: s.Bfree * bs, avail: s.Bavail * bs, files: s.Files, filesFree: s.Ffree, fsid: s.Fsid.Val}, nil
}

// skipFSTypes are pseudo, in-memory or image filesystems that do not
// represent storage capacity.
var skipFSTypes = map[string]bool{
	"proc": true, "sysfs": true, "cgroup": true, "cgroup2": true, "devtmpfs": true, "devpts": true,
	"securityfs": true, "pstore": true, "bpf": true, "tracefs": true, "debugfs": true, "configfs": true,
	"fusectl": true, "mqueue": true, "hugetlbfs": true, "autofs": true, "binfmt_misc": true,
	"rpc_pipefs": true, "nsfs": true, "efivarfs": true, "selinuxfs": true, "tmpfs": true, "ramfs": true,
	"squashfs": true, "iso9660": true, "rootfs": true,
}

// networkFSTypes can block for a long time when a server is unreachable,
// so they are skipped rather than risking a stuck statfs call.
var networkFSTypes = map[string]bool{
	"nfs": true, "nfs4": true, "cifs": true, "smb3": true, "smbfs": true,
	"fuse.sshfs": true, "ceph": true, "glusterfs": true, "fuse.glusterfs": true, "afs": true,
}

type filesystemsCollector struct {
	base
	env    collector.Env
	statfs statfsFunc
}

func newFilesystems(env collector.Env) *filesystemsCollector {
	c := &filesystemsCollector{env: env, statfs: realStatfs}
	c.base = base{collect: c.Collect}
	return c
}

func (c *filesystemsCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.filesystems", Version: version, Source: "procfs, statfs",
		Capabilities: []core.Capability{core.CapStorageFilesystems},
		Class:        collector.Sampler, DefaultInterval: 10 * time.Second, DefaultTimeout: 2 * time.Second,
	}
}

func (c *filesystemsCollector) Collect(ctx context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	b, err := readFile(c.env.ProcRoot, "self/mountinfo")
	var mounts []mount
	if err == nil {
		mounts, err = parseMountinfo(b)
	}
	if err != nil {
		res.Status[core.CapStorageFilesystems] = status(err, "the mount table")
		return res
	}

	// Bind mounts repeat the same filesystem; keep the shortest mount point
	// per device.
	sort.SliceStable(mounts, func(i, j int) bool { return len(mounts[i].mountPoint) < len(mounts[j].mountPoint) })
	seen := map[string]bool{}
	// Some filesystems (e.g. WSL drive shares over 9p) are mounted twice
	// with different device numbers. Treat mounts with the same filesystem
	// ID and identical block counts as one. A false miss only shows a
	// duplicate; it never hides a distinct filesystem.
	sameFS := map[fsStats]bool{}
	var out core.Filesystems
	denied := 0
	for _, m := range mounts {
		if ctx.Err() != nil {
			break
		}
		if skipFSTypes[m.fsType] || networkFSTypes[m.fsType] || seen[m.majorMinor] {
			continue
		}
		// Overlays are container or system layers; only a root overlay (as
		// in a container) represents the machine's storage.
		if m.fsType == "overlay" && m.mountPoint != "/" {
			continue
		}
		st, err := c.statfs(m.mountPoint)
		if err != nil {
			if errors.Is(err, fs.ErrPermission) {
				denied++
			}
			continue
		}
		if st.total == 0 {
			continue
		}
		if key := (fsStats{total: st.total, free: st.free, avail: st.avail, files: st.files, filesFree: st.filesFree, fsid: st.fsid}); st.fsid != [2]int32{} {
			if sameFS[key] {
				continue
			}
			sameFS[key] = true
		}
		seen[m.majorMinor] = true
		out = append(out, core.Filesystem{
			MountPoint: m.mountPoint, Device: m.source, Type: m.fsType, ReadOnly: m.readOnly,
			TotalBytes: st.total, FreeBytes: st.free, AvailableBytes: st.avail,
			TotalInodes: st.files, FreeInodes: st.filesFree,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MountPoint < out[j].MountPoint })
	switch {
	case len(out) > 0:
		res.Status[core.CapStorageFilesystems] = available()
		res.Records = []core.Record{out}
	case denied > 0:
		res.Status[core.CapStorageFilesystems] = core.CapabilityStatus{Availability: core.PermissionDenied,
			Reason: "Cockpit does not have permission to query filesystem capacity."}
	default:
		res.Status[core.CapStorageFilesystems] = core.CapabilityStatus{Availability: core.Unsupported,
			Reason: "No disk-backed filesystems were found."}
	}
	return res
}

// ---- block devices ----

type blockDevicesCollector struct {
	base
	env collector.Env
}

func newBlockDevices(env collector.Env) *blockDevicesCollector {
	c := &blockDevicesCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *blockDevicesCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.blockdevices", Version: version, Source: "sysfs",
		Capabilities: []core.Capability{core.CapStorageDevices},
		Class:        collector.Inventory, DefaultInterval: 5 * time.Minute, DefaultTimeout: 2 * time.Second,
	}
}

// physicalDisk reports whether a /sys/block entry is a disk worth listing
// (not a loop, RAM, compressed-swap, device-mapper or RAID device).
func physicalDisk(name string) bool {
	for _, p := range []string{"loop", "ram", "zram", "dm-", "md", "nbd", "fd"} {
		if strings.HasPrefix(name, p) {
			return false
		}
	}
	if strings.HasPrefix(name, "mmcblk") && (strings.Contains(name, "boot") || strings.Contains(name, "rpmb")) {
		return false
	}
	return true
}

// listDisks returns the physical disks under /sys/block.
func listDisks(sysRoot string) ([]string, error) {
	names, err := listDir(sysRoot, "block")
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range names {
		if physicalDisk(n) {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (c *blockDevicesCollector) Collect(context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	disks, err := listDisks(c.env.SysRoot)
	if err != nil {
		res.Status[core.CapStorageDevices] = status(err, "block device information")
		return res
	}
	var out core.BlockDevices
	for _, name := range disks {
		if d, ok := c.device(name); ok {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		res.Status[core.CapStorageDevices] = core.CapabilityStatus{Availability: core.Unsupported, Reason: "No disks were found."}
		return res
	}
	res.Status[core.CapStorageDevices] = available()
	res.Records = []core.Record{out}
	return res
}

func (c *blockDevicesCollector) device(name string) (core.BlockDevice, bool) {
	dir := filepath.Join("block", name)
	sectors, err := readUint(c.env.SysRoot, filepath.Join(dir, "size"))
	if err != nil || sectors == 0 {
		return core.BlockDevice{}, false // empty card readers, detached devices
	}
	d := core.BlockDevice{Name: name, SizeBytes: sectors * 512, Transport: transport(c.env.SysRoot, name)}
	if r, err := readTrim(c.env.SysRoot, filepath.Join(dir, "removable")); err == nil {
		d.Removable = r == "1"
	}
	if r, err := readTrim(c.env.SysRoot, filepath.Join(dir, "queue", "rotational")); err == nil {
		rot := r == "1"
		d.Rotational = &rot
	}
	d.Model = firstAttr(c.env.SysRoot, filepath.Join(dir, "device", "model"))
	d.Serial = firstAttr(c.env.SysRoot, filepath.Join(dir, "device", "serial"))
	d.WWN = firstAttr(c.env.SysRoot, filepath.Join(dir, "wwid"), filepath.Join(dir, "device", "wwid"))
	return d, true
}

func firstAttr(root string, rels ...string) string {
	for _, rel := range rels {
		if s, err := readTrim(root, rel); err == nil && s != "" {
			return s
		}
	}
	return ""
}

// transport infers the bus from the device name or its sysfs path.
func transport(sysRoot, name string) string {
	switch {
	case strings.HasPrefix(name, "nvme"):
		return "nvme"
	case strings.HasPrefix(name, "vd"):
		return "virtio"
	case strings.HasPrefix(name, "xvd"):
		return "xen"
	case strings.HasPrefix(name, "mmcblk"):
		return "mmc"
	}
	target, err := os.Readlink(filepath.Join(sysRoot, "block", name))
	if err != nil {
		return "unknown"
	}
	switch {
	case strings.Contains(target, "/usb"):
		return "usb"
	case strings.Contains(target, "/ata"):
		return "sata"
	case strings.Contains(target, "/virtio"):
		return "virtio"
	case strings.HasPrefix(name, "sd") || strings.HasPrefix(name, "sr"):
		return "scsi"
	}
	return "unknown"
}
