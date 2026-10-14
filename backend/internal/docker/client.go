// Package docker is a small client for the Docker Engine API.
//
// It talks HTTP over the daemon's Unix socket (or TCP) using only the
// standard library, and covers just the endpoints the dashboard needs.
// Reference: https://docs.docker.com/reference/api/engine/
package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client is a Docker Engine API client. It is safe for concurrent use.
type Client struct {
	http *http.Client // for normal request/response calls
	// stream has no overall timeout, for logs and pull progress.
	stream *http.Client
	base   string // e.g. "http://docker" or "http://10.0.0.5:2375"

	apiMu    sync.Mutex
	apiVer   string // daemon API version as reported, once known
	apiKnown bool
}

// Error is a non-2xx response from the daemon.
type Error struct {
	StatusCode int
	Message    string
}

func (e *Error) Error() string { return fmt.Sprintf("docker: %s (HTTP %d)", e.Message, e.StatusCode) }

// IsNotFound reports whether err is a 404 from the daemon.
func IsNotFound(err error) bool {
	var de *Error
	return errors.As(err, &de) && de.StatusCode == http.StatusNotFound
}

// StatusCode returns the daemon's HTTP status for err, or 0.
func StatusCode(err error) int {
	var de *Error
	if errors.As(err, &de) {
		return de.StatusCode
	}
	return 0
}

// New creates a client for host, which is a DOCKER_HOST style address:
// "unix:///var/run/docker.sock" (the default when empty) or "tcp://host:2375".
func New(host string) (*Client, error) {
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parse DOCKER_HOST %q: %w", host, err)
	}
	var (
		dial func(ctx context.Context, network, addr string) (net.Conn, error)
		base string
	)
	switch u.Scheme {
	case "unix":
		path := u.Path
		d := net.Dialer{Timeout: 5 * time.Second}
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) { return d.DialContext(ctx, "unix", path) }
		base = "http://docker"
	case "tcp", "http":
		d := net.Dialer{Timeout: 5 * time.Second}
		dial = d.DialContext
		base = "http://" + u.Host
	default:
		return nil, fmt.Errorf("unsupported DOCKER_HOST scheme %q (use unix:// or tcp://)", u.Scheme)
	}
	tr := &http.Transport{DialContext: dial, MaxIdleConnsPerHost: 16, IdleConnTimeout: 90 * time.Second}
	return &Client{
		http:   &http.Client{Transport: tr, Timeout: 60 * time.Second},
		stream: &http.Client{Transport: tr},
		base:   base,
	}, nil
}

func (c *Client) do(ctx context.Context, hc *http.Client, method, path string, q url.Values, body any, hdr http.Header) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, err
	}
	for k, v := range hdr {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("docker: %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotModified {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var m struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(b, &m) != nil || m.Message == "" {
			m.Message = strings.TrimSpace(string(b))
		}
		return nil, &Error{StatusCode: resp.StatusCode, Message: m.Message}
	}
	return resp, nil
}

// call runs a request and decodes a JSON response into out (if non-nil).
func (c *Client) call(ctx context.Context, method, path string, q url.Values, body, out any) error {
	resp, err := c.do(ctx, c.http, method, path, q, body, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Ping checks the daemon is reachable.
func (c *Client) Ping(ctx context.Context) error {
	return c.call(ctx, http.MethodGet, "/_ping", nil, nil, nil)
}

// Version returns the daemon's version information.
func (c *Client) Version(ctx context.Context) (Version, error) {
	var v Version
	err := c.call(ctx, http.MethodGet, "/version", nil, nil, &v)
	return v, err
}

// Info returns system-wide information.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var v Info
	err := c.call(ctx, http.MethodGet, "/info", nil, nil, &v)
	return v, err
}

// DiskUsage returns `docker system df -v` data. It can be slow on big hosts.
func (c *Client) DiskUsage(ctx context.Context) (DiskUsage, error) {
	var v DiskUsage
	err := c.call(ctx, http.MethodGet, "/system/df", nil, nil, &v)
	return v, err
}

// ---- containers

func (c *Client) ListContainers(ctx context.Context, all bool) ([]Container, error) {
	q := url.Values{}
	if all {
		q.Set("all", "1")
	}
	var out []Container
	err := c.call(ctx, http.MethodGet, "/containers/json", q, nil, &out)
	return out, err
}

func (c *Client) InspectContainer(ctx context.Context, id string) (ContainerJSON, error) {
	var out ContainerJSON
	err := c.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/json", nil, nil, &out)
	return out, err
}

// ContainerAction runs start, stop, restart, pause, unpause or kill.
func (c *Client) ContainerAction(ctx context.Context, id, action string) error {
	switch action {
	case "start", "stop", "restart", "pause", "unpause", "kill":
	default:
		return fmt.Errorf("unknown container action %q", action)
	}
	return c.call(ctx, http.MethodPost, "/containers/"+url.PathEscape(id)+"/"+action, nil, nil, nil)
}

func (c *Client) RemoveContainer(ctx context.Context, id string, force, volumes bool) error {
	q := url.Values{"force": {boolStr(force)}, "v": {boolStr(volumes)}}
	return c.call(ctx, http.MethodDelete, "/containers/"+url.PathEscape(id), q, nil, nil)
}

func (c *Client) PruneContainers(ctx context.Context) (PruneReport, error) {
	var out PruneReport
	err := c.call(ctx, http.MethodPost, "/containers/prune", nil, nil, &out)
	return out, err
}

// ContainerStats returns one stats sample without waiting for a second one
// (one-shot), so CPU % must be computed against an earlier sample.
func (c *Client) ContainerStats(ctx context.Context, id string) (StatsJSON, error) {
	var out StatsJSON
	q := url.Values{"stream": {"false"}, "one-shot": {"true"}}
	err := c.call(ctx, http.MethodGet, "/containers/"+url.PathEscape(id)+"/stats", q, nil, &out)
	return out, err
}

// LogOptions selects which log lines to read.
type LogOptions struct {
	Tail       string // number of lines, or "all"
	Follow     bool
	Timestamps bool
	Since      string
}

// ContainerLogs opens the raw log stream. When the container has no TTY the
// stream is multiplexed; use NewLogReader to split it into lines.
func (c *Client) ContainerLogs(ctx context.Context, id string, o LogOptions) (io.ReadCloser, error) {
	q := url.Values{"stdout": {"1"}, "stderr": {"1"}}
	if o.Tail != "" {
		q.Set("tail", o.Tail)
	}
	if o.Follow {
		q.Set("follow", "1")
	}
	if o.Timestamps {
		q.Set("timestamps", "1")
	}
	if o.Since != "" {
		q.Set("since", o.Since)
	}
	resp, err := c.do(ctx, c.stream, http.MethodGet, "/containers/"+url.PathEscape(id)+"/logs", q, nil, nil)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// ---- images

func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	var out []Image
	err := c.call(ctx, http.MethodGet, "/images/json", url.Values{"all": {"0"}}, nil, &out)
	return out, err
}

func (c *Client) InspectImage(ctx context.Context, id string) (json.RawMessage, error) {
	var out json.RawMessage
	err := c.call(ctx, http.MethodGet, "/images/"+url.PathEscape(id)+"/json", nil, nil, &out)
	return out, err
}

func (c *Client) RemoveImage(ctx context.Context, id string, force bool) ([]map[string]string, error) {
	var out []map[string]string
	err := c.call(ctx, http.MethodDelete, "/images/"+url.PathEscape(id), url.Values{"force": {boolStr(force)}}, nil, &out)
	return out, err
}

// PruneImages removes dangling images, or every unused image when all is true.
func (c *Client) PruneImages(ctx context.Context, all bool) (PruneReport, error) {
	var out PruneReport
	q := url.Values{}
	if all {
		q.Set("filters", `{"dangling":["false"]}`)
	}
	err := c.call(ctx, http.MethodPost, "/images/prune", q, nil, &out)
	return out, err
}

// PullImage pulls ref and waits for it to finish. registryAuth is the
// base64url-encoded X-Registry-Auth value, or "".
func (c *Client) PullImage(ctx context.Context, ref, registryAuth string) error {
	name, tag := splitRef(ref)
	q := url.Values{"fromImage": {name}, "tag": {tag}}
	hdr := http.Header{}
	if registryAuth != "" {
		hdr.Set("X-Registry-Auth", registryAuth)
	}
	resp, err := c.do(ctx, c.stream, http.MethodPost, "/images/create", q, nil, hdr)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// The body is a stream of JSON progress messages; an error can arrive
	// mid-stream with a 200 status.
	dec := json.NewDecoder(resp.Body)
	for {
		var m struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&m); err == io.EOF {
			return nil
		} else if err != nil {
			return err
		}
		if m.Error != "" {
			return &Error{StatusCode: http.StatusBadGateway, Message: m.Error}
		}
	}
}

// splitRef splits "repo:tag" (or "repo@digest") for the pull API, leaving
// a registry port such as "host:5000/repo" intact.
func splitRef(ref string) (name, tag string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	slash := strings.LastIndex(ref, "/")
	if i := strings.LastIndex(ref, ":"); i > slash {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}

// ---- volumes

func (c *Client) ListVolumes(ctx context.Context) ([]Volume, error) {
	var out struct {
		Volumes []Volume `json:"Volumes"`
	}
	err := c.call(ctx, http.MethodGet, "/volumes", nil, nil, &out)
	return out.Volumes, err
}

func (c *Client) CreateVolume(ctx context.Context, name, driver string, labels map[string]string) (Volume, error) {
	var out Volume
	body := map[string]any{"Name": name, "Driver": driver, "Labels": labels}
	err := c.call(ctx, http.MethodPost, "/volumes/create", nil, body, &out)
	return out, err
}

func (c *Client) RemoveVolume(ctx context.Context, name string, force bool) error {
	return c.call(ctx, http.MethodDelete, "/volumes/"+url.PathEscape(name), url.Values{"force": {boolStr(force)}}, nil, nil)
}

// apiVersion returns the daemon's API version, asking once and remembering
// the answer (a failed ask is retried next time).
func (c *Client) apiVersion(ctx context.Context) (string, error) {
	c.apiMu.Lock()
	defer c.apiMu.Unlock()
	if !c.apiKnown {
		v, err := c.Version(ctx)
		if err != nil {
			return "", err
		}
		c.apiVer, c.apiKnown = v.APIVersion, true
	}
	return c.apiVer, nil
}

// apiBefore reports whether API version v is known to be older than
// major.minor. An unparseable version is not known to be older.
func apiBefore(v string, major, minor int) bool {
	var ma, mi int
	if _, err := fmt.Sscanf(v, "%d.%d", &ma, &mi); err != nil {
		return false
	}
	return ma < major || ma == major && mi < minor
}

// PruneVolumes removes unused volumes. With all=false only anonymous ones
// are removed (the Docker 23+ default); all=true includes named volumes.
// Daemons before API 1.42 don't know the "all" filter but already prune
// named volumes, so there all=true sends no filter (and all=false is
// refused, since it can't be honoured).
func (c *Client) PruneVolumes(ctx context.Context, all bool) (PruneReport, error) {
	var out PruneReport
	ver, err := c.apiVersion(ctx)
	if err != nil {
		return out, err
	}
	// If the version can't be read, send the filter: older daemons ignore
	// unknown filters and prune named volumes anyway.
	modern := !apiBefore(ver, 1, 42)
	q := url.Values{}
	switch {
	case all && modern:
		q.Set("filters", `{"all":["true"]}`)
	case !all && !modern:
		return out, &Error{StatusCode: http.StatusBadRequest, Message: "this Docker version (API " + ver + ") always prunes named volumes too; use all=true"}
	}
	err = c.call(ctx, http.MethodPost, "/volumes/prune", q, nil, &out)
	return out, err
}

// ---- networks

func (c *Client) ListNetworks(ctx context.Context) ([]Network, error) {
	var out []Network
	err := c.call(ctx, http.MethodGet, "/networks", nil, nil, &out)
	return out, err
}

func (c *Client) CreateNetwork(ctx context.Context, name, driver string, internal bool) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	body := map[string]any{"Name": name, "Driver": driver, "Internal": internal}
	err := c.call(ctx, http.MethodPost, "/networks/create", nil, body, &out)
	return out.ID, err
}

func (c *Client) RemoveNetwork(ctx context.Context, id string) error {
	return c.call(ctx, http.MethodDelete, "/networks/"+url.PathEscape(id), nil, nil, nil)
}

func (c *Client) ConnectNetwork(ctx context.Context, network, container string) error {
	return c.call(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/connect", nil, map[string]any{"Container": container}, nil)
}

func (c *Client) DisconnectNetwork(ctx context.Context, network, container string, force bool) error {
	return c.call(ctx, http.MethodPost, "/networks/"+url.PathEscape(network)+"/disconnect", nil, map[string]any{"Container": container, "Force": force}, nil)
}

func (c *Client) PruneNetworks(ctx context.Context) (PruneReport, error) {
	var out PruneReport
	err := c.call(ctx, http.MethodPost, "/networks/prune", nil, nil, &out)
	return out, err
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
