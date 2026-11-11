package handlers

import (
	"net/http"
	"sort"
	"time"

	"github.com/MichaelYagi/cockpit/internal/api/httpx"
	"github.com/MichaelYagi/cockpit/internal/core"
	"github.com/MichaelYagi/cockpit/pkg/apiv1"
)

// containerNamesReason is shown instead of names and images (D24).
const containerNamesReason = "Names and images come from the container engine's API socket, which Cockpit does not use: access to it is equivalent to root."

// Containers serves GET /containers (D24).
func (h *Resources) Containers(w http.ResponseWriter, r *http.Request) {
	if !allowParams(w, r) {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, h.containers(h.Snapshots.Current(), h.Now().UTC()))
}

func (h *Resources) containers(s *core.Snapshot, now time.Time) apiv1.ContainersResponse {
	return apiv1.ContainersResponse{
		Meta: h.meta(now),
		Containers: section(s.Containers, now, func(v *[]core.ContainerMetrics) *apiv1.ContainerList {
			out := &apiv1.ContainerList{Total: len(*v), Items: make([]apiv1.Container, 0, len(*v)), NamesReason: containerNamesReason}
			for _, c := range *v {
				ac := apiv1.Container{Runtime: c.Runtime, ID: c.ID, Rootless: c.Rootless, CPUPercent: c.CPUPercent, MemoryBytes: c.MemoryBytes,
					MemoryLimitBytes: c.MemoryLimitBytes, IOReadBytesPerSecond: c.IOReadBytesPerSecond, IOWriteBytesPerSecond: c.IOWriteBytesPerSecond, PIDs: c.PIDs}
				if c.Rootless {
					uid := c.UID
					ac.UID = &uid
				}
				out.Items = append(out.Items, ac)
			}
			cpu := func(c apiv1.Container) float64 {
				if c.CPUPercent == nil {
					return -1
				}
				return *c.CPUPercent
			}
			sort.SliceStable(out.Items, func(i, j int) bool {
				if a, b := cpu(out.Items[i]), cpu(out.Items[j]); a != b {
					return a > b
				}
				return out.Items[i].ID < out.Items[j].ID
			})
			return out
		}),
	}
}
