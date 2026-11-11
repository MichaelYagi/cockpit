//go:build linux

package linux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// ---- ZFS (D25) ----

const zfsNoJSON = "Pool capacity and error counts need zpool from OpenZFS 2.3 or newer (JSON output)."

type zfsCollector struct {
	base
	env collector.Env
	run runFunc
}

func newZFS(env collector.Env) *zfsCollector {
	c := &zfsCollector{env: env, run: realRun}
	c.base = base{collect: c.Collect}
	return c
}

func (c *zfsCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.zfs", Version: version, Source: "procfs/spl, zpool",
		Capabilities: []core.Capability{core.CapStorageZFS},
		Class:        collector.Sampler, DefaultInterval: 30 * time.Second, DefaultTimeout: 10 * time.Second,
		MinInterval: 30 * time.Second,
	}
}

func (c *zfsCollector) Collect(ctx context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	kstat := filepath.Join(c.env.ProcRoot, "spl/kstat/zfs")
	entries, err := os.ReadDir(kstat)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			res.Status[core.CapStorageZFS] = core.CapabilityStatus{Availability: core.Unsupported, Reason: "ZFS is not loaded on this system."}
		} else {
			res.Status[core.CapStorageZFS] = status(err, "ZFS statistics")
		}
		return res
	}
	info := &core.ZFSInfo{Pools: []core.ZFSPool{}}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(kstat, e.Name(), "state"))
		if err != nil {
			continue
		}
		info.Pools = append(info.Pools, core.ZFSPool{Name: e.Name(), State: strings.TrimSpace(string(b))})
	}
	sort.Slice(info.Pools, func(i, j int) bool { return info.Pools[i].Name < info.Pools[j].Name })
	if b, err := os.ReadFile(filepath.Join(kstat, "arcstats")); err == nil {
		info.ARC = parseARC(b)
	}
	if len(info.Pools) > 0 {
		info.DetailReason = c.details(ctx, info.Pools)
	}
	res.Status[core.CapStorageZFS] = available()
	res.Records = []core.Record{info}
	return res
}

// details adds capacity and error counts from zpool's JSON output. It
// returns a reason when they are unavailable.
func (c *zfsCollector) details(ctx context.Context, pools []core.ZFSPool) string {
	var tool string
	ok := false
	if c.env.Tools != nil {
		tool, ok = c.env.Tools.Find("zpool")
	}
	if !ok {
		return "zpool is not installed; pool capacity and error counts are unavailable."
	}
	idx := map[string]*core.ZFSPool{}
	for i := range pools {
		idx[pools[i].Name] = &pools[i]
	}
	out, code, err := c.run(ctx, tool, "list", "-j", "--json-int")
	list, perr := parseZpoolList(out)
	if err != nil || code != 0 || perr != nil {
		return zfsNoJSON
	}
	for name, p := range list {
		if t := idx[name]; t != nil {
			t.SizeBytes, t.AllocatedBytes, t.FreeBytes = p.SizeBytes, p.AllocatedBytes, p.FreeBytes
		}
	}
	out, code, err = c.run(ctx, tool, "status", "-j", "--json-int")
	st, perr := parseZpoolStatus(out)
	if err != nil || code != 0 || perr != nil {
		return "Pool error counts are unavailable: zpool status did not return JSON."
	}
	for name, p := range st {
		if t := idx[name]; t != nil {
			t.ReadErrors, t.WriteErrors, t.ChecksumErrors = p.ReadErrors, p.WriteErrors, p.ChecksumErrors
		}
	}
	return ""
}

func parseARC(b []byte) *core.ZFSARC {
	vals := map[string]uint64{}
	for _, line := range strings.Split(string(b), "\n") {
		f := strings.Fields(line)
		if len(f) == 3 {
			if v, err := strconv.ParseUint(f[2], 10, 64); err == nil {
				vals[f[0]] = v
			}
		}
	}
	if _, ok := vals["size"]; !ok {
		return nil
	}
	return &core.ZFSARC{SizeBytes: vals["size"], TargetBytes: vals["c"], MaxBytes: vals["c_max"], Hits: vals["hits"], Misses: vals["misses"]}
}

// jsonUint accepts a JSON number or a numeric string (zpool without
// --json-int prints strings).
type jsonUint struct{ v *uint64 }

func (j *jsonUint) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if n, err := strconv.ParseUint(s, 10, 64); err == nil {
		j.v = &n
	}
	return nil
}

type zpoolJSON struct {
	OutputVersion json.RawMessage `json:"output_version"`
	Pools         map[string]struct {
		Properties map[string]struct {
			Value jsonUint `json:"value"`
		} `json:"properties"`
		Vdevs map[string]struct {
			ReadErrors     jsonUint `json:"read_errors"`
			WriteErrors    jsonUint `json:"write_errors"`
			ChecksumErrors jsonUint `json:"checksum_errors"`
		} `json:"vdevs"`
	} `json:"pools"`
}

func decodeZpool(b []byte) (*zpoolJSON, error) {
	var j zpoolJSON
	dec := json.NewDecoder(bytes.NewReader(b))
	if err := dec.Decode(&j); err != nil {
		return nil, err
	}
	if j.OutputVersion == nil {
		return nil, errors.New("not zpool JSON output")
	}
	return &j, nil
}

func parseZpoolList(b []byte) (map[string]core.ZFSPool, error) {
	j, err := decodeZpool(b)
	if err != nil {
		return nil, err
	}
	out := map[string]core.ZFSPool{}
	for name, p := range j.Pools {
		out[name] = core.ZFSPool{Name: name, SizeBytes: p.Properties["size"].Value.v,
			AllocatedBytes: p.Properties["allocated"].Value.v, FreeBytes: p.Properties["free"].Value.v}
	}
	return out, nil
}

// parseZpoolStatus reads the error counts of each pool's root vdev, which
// is keyed by the pool name and sums its children.
func parseZpoolStatus(b []byte) (map[string]core.ZFSPool, error) {
	j, err := decodeZpool(b)
	if err != nil {
		return nil, err
	}
	out := map[string]core.ZFSPool{}
	for name, p := range j.Pools {
		root, ok := p.Vdevs[name]
		if !ok {
			continue
		}
		out[name] = core.ZFSPool{Name: name, ReadErrors: root.ReadErrors.v, WriteErrors: root.WriteErrors.v, ChecksumErrors: root.ChecksumErrors.v}
	}
	return out, nil
}
