// Package stats samples CPU and memory for running containers on an
// interval and keeps a rolling history in memory.
package stats

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Rikul/DockerDashboard/backend/internal/docker"
)

// Sample is one container's usage at one moment.
type Sample struct {
	Time     time.Time `json:"ts"`
	CPUPct   float64   `json:"cpu_pct"` // % of one core, like `docker stats`
	MemBytes uint64    `json:"mem_bytes"`
	MemLimit uint64    `json:"mem_limit"`
}

// Source is the part of the Docker client the collector needs.
type Source interface {
	ListContainers(ctx context.Context, all bool) ([]docker.Container, error)
	ContainerStats(ctx context.Context, id string) (docker.StatsJSON, error)
}

type series struct {
	name    string
	prev    *docker.StatsJSON
	samples []Sample // ring, oldest first
	seen    time.Time
}

// Collector polls container stats. Start it with Run.
type Collector struct {
	src      Source
	interval time.Duration
	keep     int // samples kept per container

	mu     sync.RWMutex
	byID   map[string]*series
	notify map[chan struct{}]struct{}
}

// New makes a collector sampling every interval and keeping window of history.
func New(src Source, interval, window time.Duration) *Collector {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	keep := int(window / interval)
	if keep < 2 {
		keep = 2
	}
	return &Collector{src: src, interval: interval, keep: keep, byID: map[string]*series{}, notify: map[chan struct{}]struct{}{}}
}

// Interval is the sampling period.
func (c *Collector) Interval() time.Duration { return c.interval }

// Run samples until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	t := time.NewTicker(c.interval)
	defer t.Stop()
	for {
		c.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Collector) tick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, c.interval*2)
	defer cancel()
	list, err := c.src.ListContainers(ctx, false)
	if err != nil {
		slog.Warn("stats: list containers", "err", err)
		return
	}
	now := time.Now()
	type result struct {
		id, name string
		st       docker.StatsJSON
	}
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out []result
		sem = make(chan struct{}, 8) // bound concurrent stats calls
	)
	for _, ct := range list {
		if ct.State != "running" {
			continue
		}
		wg.Add(1)
		go func(ct docker.Container) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			st, err := c.src.ContainerStats(ctx, ct.ID)
			if err != nil {
				slog.Debug("stats: container", "id", ct.ID[:12], "err", err)
				return
			}
			mu.Lock()
			out = append(out, result{ct.ID, Name(ct.Names), st})
			mu.Unlock()
		}(ct)
	}
	wg.Wait()

	c.mu.Lock()
	for _, r := range out {
		s := c.byID[r.id]
		if s == nil {
			s = &series{}
			c.byID[r.id] = s
		}
		s.name, s.seen = r.name, now
		st := r.st
		if s.prev != nil {
			s.samples = append(s.samples, Sample{
				Time:     now,
				CPUPct:   CPUPercent(*s.prev, st),
				MemBytes: st.MemoryUsed(),
				MemLimit: st.MemoryStats.Limit,
			})
			if len(s.samples) > c.keep {
				s.samples = s.samples[len(s.samples)-c.keep:]
			}
		}
		s.prev = &st
	}
	// Forget containers not seen for a whole window.
	for id, s := range c.byID {
		if now.Sub(s.seen) > c.interval*time.Duration(c.keep) {
			delete(c.byID, id)
		}
	}
	for ch := range c.notify {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	c.mu.Unlock()
}

// CPUPercent computes usage between two samples as % of one core.
func CPUPercent(prev, cur docker.StatsJSON) float64 {
	cpuDelta := float64(cur.CPUStats.CPUUsage.TotalUsage) - float64(prev.CPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(cur.CPUStats.SystemUsage) - float64(prev.CPUStats.SystemUsage)
	if cpuDelta <= 0 || sysDelta <= 0 {
		return 0
	}
	n := float64(cur.CPUStats.OnlineCPUs)
	if n == 0 {
		n = 1
	}
	return cpuDelta / sysDelta * n * 100
}

// Latest returns the newest sample per container id.
func (c *Collector) Latest() map[string]Sample {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]Sample, len(c.byID))
	for id, s := range c.byID {
		if n := len(s.samples); n > 0 {
			out[id] = s.samples[n-1]
		}
	}
	return out
}

// Series is the history of one container.
type Series struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Samples []Sample `json:"samples"`
}

// History returns samples newer than since, busiest containers first.
func (c *Collector) History(since time.Time) []Series {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Series, 0, len(c.byID))
	for id, s := range c.byID {
		i := sort.Search(len(s.samples), func(i int) bool { return !s.samples[i].Time.Before(since) })
		if i == len(s.samples) {
			continue
		}
		out = append(out, Series{ID: id, Name: s.name, Samples: append([]Sample(nil), s.samples[i:]...)})
	}
	sort.Slice(out, func(a, b int) bool {
		la, lb := out[a].Samples[len(out[a].Samples)-1], out[b].Samples[len(out[b].Samples)-1]
		if la.CPUPct != lb.CPUPct {
			return la.CPUPct > lb.CPUPct
		}
		return out[a].Name < out[b].Name
	})
	return out
}

// Subscribe returns a channel that receives after each sampling round, and
// a function to unsubscribe.
func (c *Collector) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.notify[ch] = struct{}{}
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.notify, ch)
		c.mu.Unlock()
	}
}

// Name returns a container's primary name without the leading slash.
func Name(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimPrefix(names[0], "/")
}
