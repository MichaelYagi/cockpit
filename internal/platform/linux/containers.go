//go:build linux

package linux

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelYagi/cockpit/internal/collector"
	"github.com/MichaelYagi/cockpit/internal/core"
)

// Bounds for walking the cgroup tree.
const (
	cgroupMaxDepth  = 10
	cgroupMaxDirs   = 20000
	containersLimit = 500
)

// Container scopes under the systemd cgroup driver, and the runtime each
// prefix means. conmon scopes (libpod-conmon-<id>) are Podman's monitor
// processes, not containers.
var (
	scopePattern   = regexp.MustCompile(`^(docker|libpod|cri-containerd|crio)-([0-9a-f]{64})\.scope$`)
	hexIDPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	userServiceDir = regexp.MustCompile(`^user@([0-9]+)\.service$`)
	runtimeNames   = map[string]string{"docker": "docker", "libpod": "podman", "cri-containerd": "containerd", "crio": "cri-o"}
)

type containersCollector struct {
	base
	env collector.Env
}

func newContainers(env collector.Env) *containersCollector {
	c := &containersCollector{env: env}
	c.base = base{collect: c.Collect}
	return c
}

func (c *containersCollector) Descriptor() collector.Descriptor {
	return collector.Descriptor{
		ID: "linux.containers", Version: version, Source: "cgroup v2",
		Capabilities: []core.Capability{core.CapContainers},
		Class:        collector.Sampler, DefaultInterval: 5 * time.Second, DefaultTimeout: 2 * time.Second,
		MinInterval: 5 * time.Second,
	}
}

func (c *containersCollector) Collect(ctx context.Context) collector.Result {
	res := collector.Result{CollectedAt: c.env.Now(), Status: map[core.Capability]core.CapabilityStatus{}}
	root := filepath.Join(c.env.SysRoot, "fs/cgroup")
	if _, err := os.Stat(filepath.Join(root, "cgroup.controllers")); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			res.Status[core.CapContainers] = core.CapabilityStatus{Availability: core.Unsupported,
				Reason: "Containers are found through cgroup v2, which this system does not use."}
		} else {
			res.Status[core.CapContainers] = status(err, "the cgroup hierarchy")
		}
		return res
	}
	out := core.ContainerSamples{}
	walkCgroups(ctx, root, func(rel, name string, uid int) bool {
		runtime, id := matchContainer(rel, name)
		if id == "" {
			return true // keep descending
		}
		if len(out) < containersLimit {
			out = append(out, readContainer(filepath.Join(root, rel), runtime, id, uid))
		}
		return false // a container's own sub-cgroups are part of it
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	res.Status[core.CapContainers] = available()
	res.Records = []core.Record{out}
	return res
}

// matchContainer recognizes a container cgroup: a systemd-driver scope,
// or a cgroupfs-driver directory /docker/<id>.
func matchContainer(rel, name string) (runtime, id string) {
	if strings.Contains(name, "-conmon-") {
		return "", ""
	}
	if m := scopePattern.FindStringSubmatch(name); m != nil {
		return runtimeNames[m[1]], m[2]
	}
	if hexIDPattern.MatchString(name) && filepath.Base(filepath.Dir(rel)) == "docker" {
		return "docker", name
	}
	return "", ""
}

// walkCgroups visits cgroup directories breadth first, bounded in depth
// and count. visit returns false to skip a directory's children. uid is
// the owner when the directory is below user@<uid>.service, else -1.
func walkCgroups(ctx context.Context, root string, visit func(rel, name string, uid int) bool) {
	type dir struct {
		rel   string
		depth int
		uid   int
	}
	queue := []dir{{rel: ".", uid: -1}}
	seen := 0
	for len(queue) > 0 && seen < cgroupMaxDirs && ctx.Err() == nil {
		d := queue[0]
		queue = queue[1:]
		entries, err := os.ReadDir(filepath.Join(root, d.rel))
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() || seen >= cgroupMaxDirs {
				continue
			}
			seen++
			rel := filepath.Join(d.rel, e.Name())
			uid := d.uid
			if m := userServiceDir.FindStringSubmatch(e.Name()); m != nil {
				if n, err := strconv.Atoi(m[1]); err == nil {
					uid = n
				}
			}
			if visit(rel, e.Name(), uid) && d.depth+1 < cgroupMaxDepth {
				queue = append(queue, dir{rel: rel, depth: d.depth + 1, uid: uid})
			}
		}
	}
}

// readContainer reads one container's cgroup files. Missing files leave
// fields nil: a controller may not be enabled for that cgroup.
func readContainer(dir, runtime, id string, uid int) core.ContainerSample {
	s := core.ContainerSample{Runtime: runtime, ID: id, Rootless: uid >= 0, UID: uid}
	if b, err := os.ReadFile(filepath.Join(dir, "cpu.stat")); err == nil {
		s.CPUUsageUsec = keyedValue(string(b), "usage_usec")
	}
	s.MemoryBytes = optUint(filepath.Join(dir, "memory.current"))
	s.MemoryLimitBytes = optUint(filepath.Join(dir, "memory.max")) // "max" parses as nil
	s.PIDs = optUint(filepath.Join(dir, "pids.current"))
	if b, err := os.ReadFile(filepath.Join(dir, "io.stat")); err == nil {
		var r, w uint64
		for _, line := range strings.Split(string(b), "\n") {
			for _, f := range strings.Fields(line) {
				if v, ok := strings.CutPrefix(f, "rbytes="); ok {
					n, _ := strconv.ParseUint(v, 10, 64)
					r += n
				} else if v, ok := strings.CutPrefix(f, "wbytes="); ok {
					n, _ := strconv.ParseUint(v, 10, 64)
					w += n
				}
			}
		}
		s.IOReadBytes, s.IOWriteBytes = &r, &w
	}
	return s
}

// keyedValue returns the value of "key N" in a flat-keyed cgroup file.
func keyedValue(s, key string) uint64 {
	for _, line := range strings.Split(s, "\n") {
		if v, ok := strings.CutPrefix(line, key+" "); ok {
			n, _ := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n
		}
	}
	return 0
}

func optUint(path string) *uint64 {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil {
		return nil
	}
	return &n
}
