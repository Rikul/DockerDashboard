package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rikul/DockerDashboard/backend/internal/docker"
	"github.com/Rikul/DockerDashboard/backend/internal/registry"
	"github.com/Rikul/DockerDashboard/backend/internal/stats"
)

// ---- views: the JSON shapes the frontend reads (see frontend/src/types.ts)

type PortView struct {
	IP          string `json:"ip,omitempty"`
	PrivatePort int    `json:"private_port"`
	PublicPort  int    `json:"public_port,omitempty"`
	Type        string `json:"type"`
}

type MountView struct {
	Type        string `json:"type"`
	Name        string `json:"name,omitempty"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	RW          bool   `json:"rw"`
}

type ContainerView struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	Image         string      `json:"image"`
	ImageID       string      `json:"image_id"`
	Command       string      `json:"command"`
	Created       time.Time   `json:"created"`
	State         string      `json:"state"`
	Status        string      `json:"status"`
	Health        string      `json:"health,omitempty"` // healthy, unhealthy, starting
	ExitCode      *int        `json:"exit_code,omitempty"`
	OOMKilled     bool        `json:"oom_killed,omitempty"`
	RestartCount  int         `json:"restart_count"`
	RestartPolicy string      `json:"restart_policy,omitempty"`
	StartedAt     *time.Time  `json:"started_at,omitempty"`
	Ports         []PortView  `json:"ports"`
	Networks      []string    `json:"networks"`
	Mounts        []MountView `json:"mounts"`
	Project       string      `json:"project,omitempty"` // docker compose project
	CPUPct        *float64    `json:"cpu_pct,omitempty"`
	MemBytes      *uint64     `json:"mem_bytes,omitempty"`
	MemLimit      *uint64     `json:"mem_limit,omitempty"`
	MemLimitSet   bool        `json:"mem_limit_set"` // false: limit is the host's memory
}

type ImageView struct {
	ID         string    `json:"id"`
	ShortID    string    `json:"short_id"`
	Ref        string    `json:"ref"`
	RepoTags   []string  `json:"repo_tags"`
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	Created    time.Time `json:"created"`
	Size       int64     `json:"size"`
	Containers int       `json:"containers"`
	Dangling   bool      `json:"dangling"`
}

type VolumeView struct {
	Name       string    `json:"name"`
	Driver     string    `json:"driver"`
	Mountpoint string    `json:"mountpoint"`
	Created    time.Time `json:"created,omitzero"`
	Size       int64     `json:"size"` // -1 when unknown
	UsedBy     []string  `json:"used_by"`
	Anonymous  bool      `json:"anonymous"`
	Project    string    `json:"project,omitempty"`
}

type NetworkView struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Driver     string    `json:"driver"`
	Scope      string    `json:"scope"`
	Internal   bool      `json:"internal"`
	Subnet     string    `json:"subnet,omitempty"`
	Gateway    string    `json:"gateway,omitempty"`
	Created    time.Time `json:"created,omitzero"`
	Containers []string  `json:"containers"`
	Builtin    bool      `json:"builtin"`
	Project    string    `json:"project,omitempty"`
}

const projectLabel = "com.docker.compose.project"

// ---- system

func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	info, err := s.docker.Info(ctx)
	if err != nil {
		dockerError(w, err)
		return
	}
	ver, err := s.docker.Version(ctx)
	if err != nil {
		dockerError(w, err)
		return
	}
	resp := map[string]any{
		"name":               info.Name,
		"server_version":     info.ServerVersion,
		"api_version":        ver.APIVersion,
		"os":                 info.OperatingSystem,
		"arch":               info.Architecture,
		"kernel":             ver.KernelVersion,
		"cpus":               info.NCPU,
		"mem_total":          info.MemTotal,
		"storage_driver":     info.Driver,
		"read_only":          s.cfg.ReadOnly,
		"registry":           nil,
		"stats_interval_sec": s.stats.Interval().Seconds(),
	}
	if s.registry != nil {
		resp["registry"] = s.registry.Host()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	minutes, _ := strconv.Atoi(r.URL.Query().Get("minutes"))
	if minutes <= 0 || minutes > 60 {
		minutes = 60
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"interval_sec": s.stats.Interval().Seconds(),
		"series":       s.stats.History(time.Now().Add(-time.Duration(minutes) * time.Minute)),
	})
}

// ---- containers

// inspect returns cached inspect data; it is refreshed when the container's
// state changes or after 30 seconds (restart counts and health move on).
func (s *Server) inspect(ctx context.Context, c docker.Container) (docker.ContainerJSON, error) {
	s.inspectMu.Lock()
	e, ok := s.inspectCache[c.ID]
	s.inspectMu.Unlock()
	if ok && e.state == c.State && time.Since(e.at) < 30*time.Second {
		return e.data, nil
	}
	data, err := s.docker.InspectContainer(ctx, c.ID)
	if err != nil {
		return data, err
	}
	s.inspectMu.Lock()
	s.inspectCache[c.ID] = inspectEntry{state: c.State, at: time.Now(), data: data}
	s.inspectMu.Unlock()
	return data, nil
}

func (s *Server) containerViews(ctx context.Context) ([]ContainerView, error) {
	list, err := s.docker.ListContainers(ctx, true)
	if err != nil {
		return nil, err
	}
	latest := s.stats.Latest()
	out := make([]ContainerView, len(list))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	live := make(map[string]bool, len(list))
	for i, c := range list {
		live[c.ID] = true
		v := ContainerView{
			ID: c.ID, Name: stats.Name(c.Names), Image: c.Image, ImageID: c.ImageID, Command: c.Command,
			Created: time.Unix(c.Created, 0).UTC(), State: c.State, Status: c.Status,
			Ports: []PortView{}, Networks: []string{}, Mounts: []MountView{}, Project: c.Labels[projectLabel],
		}
		seenPort := map[string]bool{}
		for _, p := range c.Ports {
			// IPv4 and IPv6 bindings of one port are listed twice.
			k := fmt.Sprintf("%d/%d/%s", p.PrivatePort, p.PublicPort, p.Type)
			if seenPort[k] {
				continue
			}
			seenPort[k] = true
			v.Ports = append(v.Ports, PortView{IP: p.IP, PrivatePort: p.PrivatePort, PublicPort: p.PublicPort, Type: p.Type})
		}
		sort.Slice(v.Ports, func(a, b int) bool { return v.Ports[a].PrivatePort < v.Ports[b].PrivatePort })
		for n := range c.NetworkSettings.Networks {
			v.Networks = append(v.Networks, n)
		}
		sort.Strings(v.Networks)
		for _, m := range c.Mounts {
			v.Mounts = append(v.Mounts, MountView{Type: m.Type, Name: m.Name, Source: m.Source, Destination: m.Destination, RW: m.RW})
		}
		v.Health = healthFromStatus(c.Status)
		if smp, ok := latest[c.ID]; ok && c.State == "running" {
			cpu, mem, lim := smp.CPUPct, smp.MemBytes, smp.MemLimit
			v.CPUPct, v.MemBytes, v.MemLimit = &cpu, &mem, &lim
		}
		out[i] = v
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			in, err := s.inspect(ctx, c)
			if err != nil {
				return // removed meanwhile; keep the list entry
			}
			v := &out[i]
			v.RestartCount = in.RestartCount
			v.RestartPolicy = in.HostConfig.RestartPolicy.Name
			v.OOMKilled = in.State.OOMKilled
			v.MemLimitSet = in.HostConfig.Memory > 0
			// The list's Status carries the current health; the inspect may
			// be cached, so it only fills in when Status says nothing, and
			// only while running (health isn't checked otherwise).
			if v.Health == "" && c.State == "running" && in.State.Health != nil && in.State.Health.Status != "none" {
				v.Health = in.State.Health.Status
			}
			if c.State == "exited" || c.State == "dead" {
				code := in.State.ExitCode
				v.ExitCode = &code
			}
			if t, err := time.Parse(time.RFC3339Nano, in.State.StartedAt); err == nil && !t.IsZero() && t.Year() > 1 {
				v.StartedAt = &t
			}
		}()
	}
	wg.Wait()
	// Drop cache entries for containers that are gone.
	s.inspectMu.Lock()
	for id := range s.inspectCache {
		if !live[id] {
			delete(s.inspectCache, id)
		}
	}
	s.inspectMu.Unlock()
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out, nil
}

var healthRe = regexp.MustCompile(`\((healthy|unhealthy|health: starting)\)`)

func healthFromStatus(status string) string {
	m := healthRe.FindStringSubmatch(status)
	if m == nil {
		return ""
	}
	if m[1] == "health: starting" {
		return "starting"
	}
	return m[1]
}

func (s *Server) handleContainers(w http.ResponseWriter, r *http.Request) {
	out, err := s.containerViews(r.Context())
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleContainerInspect(w http.ResponseWriter, r *http.Request) {
	// Return a trimmed inspect (state, restart policy, limits). Environment
	// variables are deliberately not included: they often hold secrets.
	data, err := s.docker.InspectContainer(r.Context(), r.PathValue("id"))
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, data)
}

func (s *Server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	id, action := r.PathValue("id"), r.PathValue("action")
	switch action {
	case "start", "stop", "restart", "pause", "unpause", "kill":
	default:
		writeError(w, http.StatusNotFound, "unknown action "+strconv.Quote(action))
		return
	}
	if err := s.docker.ContainerAction(r.Context(), id, action); err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleContainerRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.docker.RemoveContainer(r.Context(), r.PathValue("id"), queryBool(r, "force"), queryBool(r, "volumes")); err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handleContainersPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.docker.PruneContainers(r.Context())
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handleContainerLogs streams logs as Server-Sent Events: each event's data
// is a JSON LogLine. With follow=0 the stream ends after the tail.
func (s *Server) handleContainerLogs(w http.ResponseWriter, r *http.Request) {
	// End the stream when the client goes away or the server shuts down.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	defer context.AfterFunc(s.streamCtx(), cancel)()
	id := r.PathValue("id")
	in, err := s.docker.InspectContainer(ctx, id)
	if err != nil {
		dockerError(w, err)
		return
	}
	tail := r.URL.Query().Get("tail")
	if n, err := strconv.Atoi(tail); err != nil || n < 0 || n > 5000 {
		tail = "200"
	}
	follow := r.URL.Query().Get("follow") != "0"
	rc, err := s.docker.ContainerLogs(ctx, id, docker.LogOptions{Tail: tail, Follow: follow, Timestamps: queryBool(r, "timestamps")})
	if err != nil {
		dockerError(w, err)
		return
	}
	defer rc.Close()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // let nginx pass events through
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	lines := make(chan docker.LogLine, 256)
	errc := make(chan error, 1)
	go func() {
		lr := docker.NewLogReader(rc, in.Config.Tty)
		for {
			l, err := lr.Next()
			if err != nil {
				errc <- err
				return
			}
			select {
			case lines <- l:
			case <-ctx.Done():
				return
			}
		}
	}()
	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-ctx.Done():
			if s.streamCtx().Err() != nil {
				fmt.Fprint(w, "event: end\ndata: {\"reason\":\"the dashboard server is shutting down\"}\n\n")
				flusher.Flush()
			}
			return
		case l := <-lines:
			b, _ := json.Marshal(l)
			fmt.Fprintf(w, "data: %s\n\n", b)
			// Batch whatever else is ready before flushing.
			for n := 0; n < 200; n++ {
				select {
				case l := <-lines:
					b, _ := json.Marshal(l)
					fmt.Fprintf(w, "data: %s\n\n", b)
					continue
				default:
				}
				break
			}
			flusher.Flush()
		case err := <-errc:
			// Drain lines already read, then say why the stream ended.
			for {
				select {
				case l := <-lines:
					b, _ := json.Marshal(l)
					fmt.Fprintf(w, "data: %s\n\n", b)
					continue
				default:
				}
				break
			}
			reason := "end"
			if s.streamCtx().Err() != nil {
				reason = "the dashboard server is shutting down"
			} else if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
				reason = err.Error()
			}
			b, _ := json.Marshal(map[string]string{"reason": reason})
			fmt.Fprintf(w, "event: end\ndata: %s\n\n", b)
			flusher.Flush()
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

// ---- images

func (s *Server) handleImages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	imgs, err := s.docker.ListImages(ctx)
	if err != nil {
		dockerError(w, err)
		return
	}
	cts, err := s.docker.ListContainers(ctx, true)
	if err != nil {
		dockerError(w, err)
		return
	}
	used := map[string]int{}
	for _, c := range cts {
		used[c.ImageID]++
	}
	out := make([]ImageView, 0, len(imgs))
	for _, im := range imgs {
		v := ImageView{
			ID: im.ID, ShortID: shortID(im.ID), Created: time.Unix(im.Created, 0).UTC(), Size: im.Size,
			Containers: used[im.ID], RepoTags: []string{},
		}
		for _, t := range im.RepoTags {
			if t != "<none>:<none>" {
				v.RepoTags = append(v.RepoTags, t)
			}
		}
		if len(v.RepoTags) == 0 {
			v.Dangling = true
			v.Ref = "<none>@" + v.ShortID
			v.Repository, v.Tag = "<none>", "<none>"
			if len(im.RepoDigests) > 0 {
				v.Repository = strings.SplitN(im.RepoDigests[0], "@", 2)[0]
			}
		} else {
			v.Ref = v.RepoTags[0]
			i := strings.LastIndex(v.Ref, ":")
			if i > strings.LastIndex(v.Ref, "/") {
				v.Repository, v.Tag = v.Ref[:i], v.Ref[i+1:]
			} else {
				v.Repository, v.Tag = v.Ref, ""
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Size > out[b].Size })
	writeJSON(w, http.StatusOK, out)
}

func shortID(id string) string {
	id = strings.TrimPrefix(id, "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (s *Server) handleImageInspect(w http.ResponseWriter, r *http.Request) {
	data, err := s.docker.InspectImage(r.Context(), r.PathValue("id"))
	if err != nil {
		dockerError(w, err)
		return
	}
	out, err := redactImageInspect(data)
	if err != nil {
		writeError(w, http.StatusBadGateway, "decode image inspect: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// redactImageInspect drops the environment variables from a raw image
// inspect, matching the container inspect: values baked into an image with
// ENV often hold secrets, and read-only viewers must not see them.
func redactImageInspect(data []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	for _, key := range []string{"Config", "ContainerConfig"} {
		if cfg, ok := m[key].(map[string]any); ok {
			delete(cfg, "Env")
		}
	}
	return m, nil
}

func (s *Server) handleImageRemove(w http.ResponseWriter, r *http.Request) {
	out, err := s.docker.RemoveImage(r.Context(), r.PathValue("id"), queryBool(r, "force"))
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleImagePull(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Ref string `json:"ref"`
	}
	if err := decodeBody(r, &body); err != nil || strings.TrimSpace(body.Ref) == "" {
		writeError(w, http.StatusBadRequest, `body must be {"ref": "image:tag"}`)
		return
	}
	ref := strings.TrimSpace(body.Ref)
	auth := ""
	if s.registry != nil && strings.HasPrefix(ref, s.registry.Host()+"/") {
		auth = s.registry.AuthHeader()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	if err := s.docker.PullImage(ctx, ref, auth); err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "pulled", "ref": ref})
}

func (s *Server) handleImagesPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.docker.PruneImages(r.Context(), queryBool(r, "all"))
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---- volumes

// diskUsage caches /system/df, which walks volume directories and can take
// seconds on big hosts.
func (s *Server) diskUsage(ctx context.Context, fresh bool) (*docker.DiskUsage, error) {
	s.dfMu.Lock()
	defer s.dfMu.Unlock()
	if !fresh && s.dfCache != nil && time.Since(s.dfCached) < 30*time.Second {
		return s.dfCache, nil
	}
	df, err := s.docker.DiskUsage(ctx)
	if err != nil {
		return nil, err
	}
	s.dfCache, s.dfCached = &df, time.Now()
	return s.dfCache, nil
}

func (s *Server) invalidateDiskUsage() {
	s.dfMu.Lock()
	s.dfCache = nil
	s.dfMu.Unlock()
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *Server) handleVolumes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	vols, err := s.docker.ListVolumes(ctx)
	if err != nil {
		dockerError(w, err)
		return
	}
	cts, err := s.docker.ListContainers(ctx, true)
	if err != nil {
		dockerError(w, err)
		return
	}
	sizes := map[string]int64{}
	if df, err := s.diskUsage(ctx, queryBool(r, "refresh")); err == nil {
		for _, v := range df.Volumes {
			if v.UsageData != nil {
				sizes[v.Name] = v.UsageData.Size
			}
		}
	}
	usedBy := map[string][]string{}
	for _, c := range cts {
		for _, m := range c.Mounts {
			if m.Type == "volume" && m.Name != "" {
				usedBy[m.Name] = append(usedBy[m.Name], stats.Name(c.Names))
			}
		}
	}
	out := make([]VolumeView, 0, len(vols))
	for _, v := range vols {
		size, ok := sizes[v.Name]
		if !ok {
			size = -1
		}
		vv := VolumeView{
			Name: v.Name, Driver: v.Driver, Mountpoint: v.Mountpoint, Size: size,
			UsedBy: usedBy[v.Name], Project: v.Labels[projectLabel],
			Anonymous: v.Labels["com.docker.volume.anonymous"] != "" || hex64.MatchString(v.Name),
		}
		if vv.UsedBy == nil {
			vv.UsedBy = []string{}
		}
		sort.Strings(vv.UsedBy)
		if t, err := time.Parse(time.RFC3339, v.CreatedAt); err == nil {
			vv.Created = t.UTC()
		}
		out = append(out, vv)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleVolumeCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name   string `json:"name"`
		Driver string `json:"driver"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Driver == "" {
		body.Driver = "local"
	}
	v, err := s.docker.CreateVolume(r.Context(), body.Name, body.Driver, nil)
	if err != nil {
		dockerError(w, err)
		return
	}
	s.invalidateDiskUsage()
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleVolumeRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.docker.RemoveVolume(r.Context(), r.PathValue("name"), queryBool(r, "force")); err != nil {
		dockerError(w, err)
		return
	}
	s.invalidateDiskUsage()
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) handleVolumesPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.docker.PruneVolumes(r.Context(), queryBool(r, "all"))
	if err != nil {
		dockerError(w, err)
		return
	}
	s.invalidateDiskUsage()
	writeJSON(w, http.StatusOK, rep)
}

// ---- networks

var builtinNetworks = map[string]bool{"bridge": true, "host": true, "none": true}

func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	nets, err := s.docker.ListNetworks(ctx)
	if err != nil {
		dockerError(w, err)
		return
	}
	// Attached containers come from one container list, rather than an
	// inspect per network on every poll.
	cs, err := s.docker.ListContainers(ctx, true)
	if err != nil {
		dockerError(w, err)
		return
	}
	members := map[string][]string{} // network id -> container names
	for _, c := range cs {
		for _, ep := range c.NetworkSettings.Networks {
			if ep.NetworkID != "" {
				members[ep.NetworkID] = append(members[ep.NetworkID], stats.Name(c.Names))
			}
		}
	}
	out := make([]NetworkView, len(nets))
	for i, n := range nets {
		v := NetworkView{
			ID: n.ID, Name: n.Name, Driver: n.Driver, Scope: n.Scope, Internal: n.Internal,
			Containers: []string{}, Builtin: builtinNetworks[n.Name], Project: n.Labels[projectLabel],
		}
		if len(n.IPAM.Config) > 0 {
			v.Subnet, v.Gateway = n.IPAM.Config[0].Subnet, n.IPAM.Config[0].Gateway
		}
		if t, err := time.Parse(time.RFC3339Nano, n.Created); err == nil {
			v.Created = t.UTC()
		}
		v.Containers = append(v.Containers, members[n.ID]...)
		sort.Strings(v.Containers)
		out[i] = v
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleNetworkCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name     string `json:"name"`
		Driver   string `json:"driver"`
		Internal bool   `json:"internal"`
	}
	if err := decodeBody(r, &body); err != nil || body.Name == "" {
		writeError(w, http.StatusBadRequest, `body must be {"name": "...", "driver": "bridge", "internal": false}`)
		return
	}
	if body.Driver == "" {
		body.Driver = "bridge"
	}
	id, err := s.docker.CreateNetwork(r.Context(), body.Name, body.Driver, body.Internal)
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) handleNetworkRemove(w http.ResponseWriter, r *http.Request) {
	if err := s.docker.RemoveNetwork(r.Context(), r.PathValue("id")); err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

func (s *Server) networkMembership(w http.ResponseWriter, r *http.Request, connect bool) {
	var body struct {
		Container string `json:"container"`
		Force     bool   `json:"force"`
	}
	if err := decodeBody(r, &body); err != nil || body.Container == "" {
		writeError(w, http.StatusBadRequest, `body must be {"container": "name or id"}`)
		return
	}
	var err error
	if connect {
		err = s.docker.ConnectNetwork(r.Context(), r.PathValue("id"), body.Container)
	} else {
		err = s.docker.DisconnectNetwork(r.Context(), r.PathValue("id"), body.Container, body.Force)
	}
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleNetworkConnect(w http.ResponseWriter, r *http.Request) {
	s.networkMembership(w, r, true)
}

func (s *Server) handleNetworkDisconnect(w http.ResponseWriter, r *http.Request) {
	s.networkMembership(w, r, false)
}

func (s *Server) handleNetworksPrune(w http.ResponseWriter, r *http.Request) {
	rep, err := s.docker.PruneNetworks(r.Context())
	if err != nil {
		dockerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---- registry

func (s *Server) handleRegistry(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeJSON(w, http.StatusOK, map[string]any{"configured": false, "tags": []any{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	l, err := s.registry.List(ctx, time.Minute, queryBool(r, "refresh"))
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "host": s.registry.Host(), "tags": l.Tags, "problems": l.Problems})
}

func (s *Server) handleRegistryDelete(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		writeError(w, http.StatusNotFound, "no registry configured")
		return
	}
	repo, tag := r.PathValue("repo"), r.URL.Query().Get("tag")
	if repo == "" || tag == "" {
		writeError(w, http.StatusBadRequest, "repository path and ?tag= are required")
		return
	}
	if err := s.registry.DeleteTag(r.Context(), repo, tag); err != nil {
		status := http.StatusBadGateway
		switch {
		case errors.Is(err, registry.ErrNotFound):
			status = http.StatusNotFound
		case errors.Is(err, registry.ErrInvalidName):
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
