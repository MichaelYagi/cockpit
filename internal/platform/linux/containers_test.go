//go:build linux

package linux

import (
	"context"
	"strings"
	"testing"

	"github.com/MichaelYagi/cockpit/internal/core"
)

func hexID(c byte) string { return strings.Repeat(string(c), 64) }

func TestContainers(t *testing.T) {
	cg := "sys/fs/cgroup/"
	docker := cg + "system.slice/docker-" + hexID('a') + ".scope/"
	podman := cg + "machine.slice/libpod-" + hexID('b') + ".scope/"
	rootless := cg + "user.slice/user-1000.slice/user@1000.service/user.slice/libpod-" + hexID('c') + ".scope/"
	cri := cg + "kubepods.slice/kubepods-burstable.slice/cri-containerd-" + hexID('d') + ".scope/"
	cgroupfs := cg + "docker/" + hexID('e') + "/"
	root := tree(t, map[string]string{
		cg + "cgroup.controllers":     "cpu memory io pids\n",
		docker + "cpu.stat":           "usage_usec 5000000\nuser_usec 4000000\n",
		docker + "memory.current":     "104857600\n",
		docker + "memory.max":         "max\n",
		docker + "pids.current":       "7\n",
		docker + "io.stat":            "8:0 rbytes=1000 wbytes=2000 rios=1 wios=2\n8:16 rbytes=24 wbytes=0\n",
		podman + "cpu.stat":           "usage_usec 10\n",
		podman + "memory.max":         "536870912\n",
		podman + "container/cpu.stat": "usage_usec 9\n", // part of the container, not another one
		cg + "machine.slice/libpod-conmon-" + hexID('b') + ".scope/cpu.stat": "usage_usec 1\n",
		rootless + "cpu.stat":                    "usage_usec 3\n", // no memory or io controller delegated
		cri + "cpu.stat":                         "usage_usec 4\n",
		cgroupfs + "cpu.stat":                    "usage_usec 6\n",
		cg + "system.slice/ssh.service/cpu.stat": "usage_usec 99\n",
	})
	r := newContainers(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapContainers, core.Available)
	got := record[core.ContainerSamples](t, r)
	if len(got) != 5 {
		t.Fatalf("containers %+v", got)
	}
	by := map[string]core.ContainerSample{}
	for _, c := range got {
		by[c.ID[:1]] = c
	}
	d := by["a"]
	if d.Runtime != "docker" || d.Rootless || d.UID != -1 || d.CPUUsageUsec != 5000000 || *d.MemoryBytes != 100<<20 || d.MemoryLimitBytes != nil ||
		*d.PIDs != 7 || *d.IOReadBytes != 1024 || *d.IOWriteBytes != 2000 {
		t.Errorf("docker %+v", d)
	}
	if p := by["b"]; p.Runtime != "podman" || p.CPUUsageUsec != 10 || *p.MemoryLimitBytes != 512<<20 || p.MemoryBytes != nil {
		t.Errorf("podman %+v", p)
	}
	if rl := by["c"]; rl.Runtime != "podman" || !rl.Rootless || rl.UID != 1000 || rl.MemoryBytes != nil || rl.IOReadBytes != nil {
		t.Errorf("rootless %+v", rl)
	}
	if by["d"].Runtime != "containerd" || by["e"].Runtime != "docker" {
		t.Errorf("runtimes %s %s", by["d"].Runtime, by["e"].Runtime)
	}
}

func TestContainersNone(t *testing.T) {
	root := tree(t, map[string]string{"sys/fs/cgroup/cgroup.controllers": "cpu\n", "sys/fs/cgroup/system.slice/ssh.service/cpu.stat": "usage_usec 1\n"})
	r := newContainers(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapContainers, core.Available)
	if got := record[core.ContainerSamples](t, r); len(got) != 0 {
		t.Errorf("%+v", got)
	}
}

func TestContainersCgroupV1(t *testing.T) {
	root := tree(t, map[string]string{"sys/fs/cgroup/cpu/tasks": "1\n"})
	r := newContainers(env(root)).Collect(context.Background())
	wantStatus(t, r, core.CapContainers, core.Unsupported)
	if !strings.Contains(r.Status[core.CapContainers].Reason, "cgroup v2") {
		t.Errorf("reason %q", r.Status[core.CapContainers].Reason)
	}
}
