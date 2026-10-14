package stats

import (
	"context"
	"testing"
	"time"

	"github.com/Rikul/DockerDashboard/backend/internal/docker"
)

func sample(total, system uint64, cpus uint32) docker.StatsJSON {
	var s docker.StatsJSON
	s.CPUStats.CPUUsage.TotalUsage = total
	s.CPUStats.SystemUsage = system
	s.CPUStats.OnlineCPUs = cpus
	return s
}

func TestCPUPercent(t *testing.T) {
	// The container used 1/4 of all CPU time on a 4-CPU host: one full core.
	got := CPUPercent(sample(0, 0, 4), sample(250, 1000, 4))
	if got != 100 {
		t.Fatalf("got %v, want 100", got)
	}
	if got := CPUPercent(sample(10, 10, 4), sample(10, 10, 4)); got != 0 {
		t.Fatalf("no time passed: got %v, want 0", got)
	}
}

type fakeSource struct{ n uint64 }

// ListContainers runs once per tick, before the concurrent stats calls, so
// it advances the fake clock.
func (f *fakeSource) ListContainers(context.Context, bool) ([]docker.Container, error) {
	f.n++
	return []docker.Container{
		{ID: "aaaaaaaaaaaaaaaa", Names: []string{"/busy"}, State: "running"},
		{ID: "bbbbbbbbbbbbbbbb", Names: []string{"/idle"}, State: "running"},
		{ID: "cccccccccccccccc", Names: []string{"/stopped"}, State: "exited"},
	}, nil
}

func (f *fakeSource) ContainerStats(_ context.Context, id string) (docker.StatsJSON, error) {
	s := sample(0, f.n*1000, 2)
	if id == "aaaaaaaaaaaaaaaa" {
		s.CPUStats.CPUUsage.TotalUsage = f.n * 250 // a quarter of 2 CPUs' time: half a core (50%)
	}
	s.MemoryStats.Usage, s.MemoryStats.Limit = 100, 1000
	s.MemoryStats.Stats = map[string]uint64{"inactive_file": 40}
	return s, nil
}

func TestCollectorHistory(t *testing.T) {
	c := New(&fakeSource{}, time.Second, time.Minute)
	for i := 0; i < 3; i++ {
		c.tick(context.Background())
	}
	h := c.History(time.Time{})
	if len(h) != 2 {
		t.Fatalf("want 2 running series, got %d", len(h))
	}
	if h[0].Name != "busy" || len(h[0].Samples) != 2 {
		t.Fatalf("want busy first with 2 samples, got %+v", h[0])
	}
	if s := h[0].Samples[1]; s.MemBytes != 60 || s.MemLimit != 1000 || s.CPUPct <= 0 {
		t.Fatalf("bad sample %+v", s)
	}
	if len(c.Latest()) != 2 {
		t.Fatal("latest should have both running containers")
	}
}
