// Package registry is a minimal client for an OCI / Docker Registry HTTP
// API v2 registry (distribution, Harbor, GitLab, GHCR, ...), enough to list
// repositories and tags, read manifests and delete tags.
package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Config says which registry to browse. URL empty disables the feature.
type Config struct {
	URL      string // e.g. https://registry.example.com
	Username string
	Password string
	// MaxRepos and MaxTags bound how much one listing reads: the newest
	// MaxTags tags (by image creation time) of each of MaxRepos repositories.
	// Ranking by creation time means reading each tag's manifest, so at most
	// MaxScanTags tags per repository are examined.
	MaxRepos    int
	MaxTags     int
	MaxScanTags int
}

// Client talks to one registry.
type Client struct {
	cfg  Config
	base *url.URL
	http *http.Client

	mu     sync.Mutex
	tokens map[string]string // scope -> bearer token

	cacheMu sync.Mutex
	// scanMu lets one listing scan run at a time; cacheMu only guards the
	// fields below, so it is never held across network calls.
	scanMu        sync.Mutex
	cache         Listing
	cachedAt      time.Time
	cacheComplete bool
	cacheGen      uint64 // bumped when the cache is invalidated
}

// failedListingTTL is how long an incomplete listing is served from cache
// before the registry is tried again.
const failedListingTTL = 10 * time.Second

// Listing is what List returns: the tags, plus any repository that could not
// be read completely, so a partial listing is never mistaken for a full one.
type Listing struct {
	Tags     []Tag    `json:"tags"`
	Problems []string `json:"problems,omitempty"`
}

// Tag is one tag of one repository, as the dashboard shows it.
type Tag struct {
	Ref        string    `json:"ref"` // host/repo:tag
	Repository string    `json:"repository"`
	Tag        string    `json:"tag"`
	Digest     string    `json:"digest"`
	MediaType  string    `json:"media_type"`
	Size       int64     `json:"size"` // config + layers, compressed
	Created    time.Time `json:"created,omitzero"`
	Platforms  []string  `json:"platforms,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// New returns nil when cfg.URL is empty.
func New(cfg Config) (*Client, error) {
	if cfg.URL == "" {
		return nil, nil
	}
	if !strings.Contains(cfg.URL, "://") {
		cfg.URL = "https://" + cfg.URL
	}
	u, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse REGISTRY_URL: %w", err)
	}
	if cfg.MaxRepos <= 0 {
		cfg.MaxRepos = 100
	}
	if cfg.MaxTags <= 0 {
		cfg.MaxTags = 25
	}
	if cfg.MaxScanTags < cfg.MaxTags {
		cfg.MaxScanTags = max(200, cfg.MaxTags)
	}
	return &Client{cfg: cfg, base: u, http: &http.Client{Timeout: 30 * time.Second}, tokens: map[string]string{}}, nil
}

// Host is the registry's host[:port], as used in image references.
func (c *Client) Host() string { return c.base.Host }

// AuthHeader returns the X-Registry-Auth value the Docker daemon expects for
// pulls from this registry, or "" without credentials.
func (c *Client) AuthHeader() string {
	if c.cfg.Username == "" {
		return ""
	}
	b, _ := json.Marshal(map[string]string{"username": c.cfg.Username, "password": c.cfg.Password, "serveraddress": c.base.Host})
	return base64.URLEncoding.EncodeToString(b)
}

// ErrNotFound is returned for a missing repository, tag or manifest.
var ErrNotFound = errors.New("registry: not found")

const manifestAccept = "application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json"

func (c *Client) request(ctx context.Context, method, path, scope string, hdr http.Header) (*http.Response, error) {
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range hdr {
			req.Header[k] = v
		}
		c.mu.Lock()
		tok := c.tokens[scope]
		c.mu.Unlock()
		switch {
		case tok != "":
			req.Header.Set("Authorization", "Bearer "+tok)
		case c.cfg.Username != "":
			req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			challenge := resp.Header.Get("WWW-Authenticate")
			resp.Body.Close()
			if strings.HasPrefix(strings.ToLower(challenge), "bearer ") {
				if err := c.fetchToken(ctx, challenge, scope); err != nil {
					return nil, err
				}
				continue
			}
			return nil, fmt.Errorf("registry: unauthorized (check REGISTRY_USERNAME / REGISTRY_PASSWORD)")
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return nil, ErrNotFound
		}
		if resp.StatusCode >= 300 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			return nil, fmt.Errorf("registry: %s %s: HTTP %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(b)))
		}
		return resp, nil
	}
	return nil, fmt.Errorf("registry: unauthorized")
}

// fetchToken follows a Bearer challenge: realm="...",service="...",scope="...".
func (c *Client) fetchToken(ctx context.Context, challenge, scope string) error {
	params := parseChallenge(challenge[len("bearer "):])
	realm := params["realm"]
	if realm == "" {
		return fmt.Errorf("registry: bearer challenge without realm")
	}
	u, err := url.Parse(realm)
	if err != nil {
		return fmt.Errorf("registry: bad token realm %q: %w", realm, err)
	}
	q := u.Query() // keep any parameters the realm already carries
	if s := params["service"]; s != "" {
		q.Set("service", s)
	}
	if scope != "" {
		q.Set("scope", scope)
	} else if s := params["scope"]; s != "" {
		q.Set("scope", s)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	if c.cfg.Username != "" {
		req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry: token request: HTTP %d", resp.StatusCode)
	}
	var t struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return err
	}
	if t.Token == "" {
		t.Token = t.AccessToken
	}
	c.mu.Lock()
	c.tokens[scope] = t.Token
	c.mu.Unlock()
	return nil
}

func parseChallenge(s string) map[string]string {
	out := map[string]string{}
	for len(s) > 0 {
		s = strings.TrimLeft(s, " ,")
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			break
		}
		key := strings.ToLower(strings.TrimSpace(s[:eq]))
		s = s[eq+1:]
		var val string
		if strings.HasPrefix(s, `"`) {
			end := strings.IndexByte(s[1:], '"')
			if end < 0 {
				val, s = s[1:], ""
			} else {
				val, s = s[1:end+1], s[end+2:]
			}
		} else {
			end := strings.IndexByte(s, ',')
			if end < 0 {
				val, s = s, ""
			} else {
				val, s = s[:end], s[end:]
			}
		}
		out[key] = val
	}
	return out
}

func (c *Client) getJSON(ctx context.Context, path, scope string, out any) error {
	_, err := c.getJSONPage(ctx, path, scope, out)
	return err
}

// getJSONPage is getJSON for paginated endpoints: it also returns the path of
// the next page from the Link header, or "" on the last page.
func (c *Client) getJSONPage(ctx context.Context, path, scope string, out any) (string, error) {
	resp, err := c.request(ctx, http.MethodGet, path, scope, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(out); err != nil {
		return "", err
	}
	return nextLink(resp.Header.Get("Link")), nil
}

// nextLink extracts the path and query of a `<url>; rel="next"` Link header.
func nextLink(h string) string {
	for _, part := range strings.Split(h, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok || !strings.Contains(strings.ReplaceAll(params, " ", ""), `rel="next"`) {
			continue
		}
		target = strings.TrimSpace(target)
		if !strings.HasPrefix(target, "<") || !strings.HasSuffix(target, ">") {
			continue
		}
		u, err := url.Parse(target[1 : len(target)-1])
		if err != nil || u.Path == "" {
			continue
		}
		return u.RequestURI()
	}
	return ""
}

// Repositories lists repository names (GET /v2/_catalog).
func (c *Client) Repositories(ctx context.Context) ([]string, error) {
	repos, _, err := c.repositories(ctx)
	return repos, err
}

// repositories reads the catalog, following its pagination, up to MaxRepos
// names. more reports whether the registry has further repositories.
func (c *Client) repositories(ctx context.Context) (repos []string, more bool, err error) {
	path := fmt.Sprintf("/v2/_catalog?n=%d", c.cfg.MaxRepos)
	for page := 0; path != ""; page++ {
		if len(repos) >= c.cfg.MaxRepos {
			return repos[:c.cfg.MaxRepos], true, nil
		}
		if page >= 1000 {
			return repos, true, nil
		}
		var out struct {
			Repositories []string `json:"repositories"`
		}
		next, err := c.getJSONPage(ctx, path, "registry:catalog:*", &out)
		if err != nil {
			return repos, false, err
		}
		repos = append(repos, out.Repositories...)
		path = next
	}
	if len(repos) > c.cfg.MaxRepos {
		return repos[:c.cfg.MaxRepos], true, nil
	}
	return repos, false, nil
}

// Tags lists all of a repository's tags, following the registry's
// pagination (Link: <...>; rel="next").
func (c *Client) Tags(ctx context.Context, repo string) ([]string, error) {
	var all []string
	path := "/v2/" + repo + "/tags/list"
	for page := 0; path != ""; page++ {
		if page >= 1000 {
			return all, fmt.Errorf("registry: %s: too many tag pages", repo)
		}
		var out struct {
			Tags []string `json:"tags"`
		}
		next, err := c.getJSONPage(ctx, path, "repository:"+repo+":pull", &out)
		if err != nil {
			return all, err
		}
		all = append(all, out.Tags...)
		path = next
	}
	return all, nil
}

// versionLess orders tag names so that numeric runs compare as numbers
// ("v9" < "v10"), which approximates release order far better than plain
// lexical order when there are too many tags to read every manifest.
func versionLess(a, b string) bool {
	for a != "" && b != "" {
		da, db := digitPrefix(a), digitPrefix(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) < len(nb)
			}
			if na != nb {
				return na < nb
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func digitPrefix(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}

type descriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	Platform  *struct {
		OS           string `json:"os"`
		Architecture string `json:"architecture"`
		Variant      string `json:"variant"`
	} `json:"platform,omitempty"`
}

type manifest struct {
	MediaType string       `json:"mediaType"`
	Config    descriptor   `json:"config"`
	Layers    []descriptor `json:"layers"`
	Manifests []descriptor `json:"manifests"` // index / manifest list
}

func (c *Client) manifest(ctx context.Context, repo, ref string) (manifest, string, error) {
	resp, err := c.request(ctx, http.MethodGet, "/v2/"+repo+"/manifests/"+ref, "repository:"+repo+":pull", http.Header{"Accept": {manifestAccept}})
	if err != nil {
		return manifest{}, "", err
	}
	defer resp.Body.Close()
	var m manifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&m); err != nil {
		return m, "", err
	}
	if m.MediaType == "" {
		m.MediaType = resp.Header.Get("Content-Type")
	}
	return m, resp.Header.Get("Docker-Content-Digest"), nil
}

// Describe reads one tag's manifest, size and creation time. A failure is
// recorded in the returned Tag's Error.
func (c *Client) Describe(ctx context.Context, repo, tag string) Tag {
	t, _ := c.describe(ctx, repo, tag)
	return t
}

// describe is Describe that also returns the error it recorded, if any.
func (c *Client) describe(ctx context.Context, repo, tag string) (Tag, error) {
	t := Tag{Ref: c.base.Host + "/" + repo + ":" + tag, Repository: repo, Tag: tag}
	m, digest, err := c.manifest(ctx, repo, tag)
	if err != nil {
		t.Error = err.Error()
		return t, err
	}
	t.Digest, t.MediaType = digest, m.MediaType
	if len(m.Manifests) > 0 { // multi-platform: describe linux/amd64, else the first
		pick := m.Manifests[0]
		for _, d := range m.Manifests {
			if d.Platform == nil || d.Platform.OS == "unknown" {
				continue // attestation manifests
			}
			p := d.Platform.OS + "/" + d.Platform.Architecture
			if d.Platform.Variant != "" {
				p += "/" + d.Platform.Variant
			}
			t.Platforms = append(t.Platforms, p)
			if p == "linux/amd64" {
				pick = d
			}
		}
		if m, _, err = c.manifest(ctx, repo, pick.Digest); err != nil {
			t.Error = err.Error()
			return t, err
		}
	}
	t.Size = m.Config.Size
	for _, l := range m.Layers {
		t.Size += l.Size
	}
	if m.Config.Digest != "" {
		var cfg struct {
			Created time.Time `json:"created"`
		}
		err := c.getJSON(ctx, "/v2/"+repo+"/blobs/"+m.Config.Digest, "repository:"+repo+":pull", &cfg)
		if err != nil {
			// Created is only used for display and ranking, so the tag
			// isn't marked unreadable, but the error is returned so a
			// transient failure keeps the listing from being cached.
			return t, err
		}
		t.Created = cfg.Created
	}
	return t, nil
}

// List describes the newest MaxTags tags of each repository (bounded by
// MaxRepos), cached for ttl. Recency comes from each image's creation time,
// so tags are described before they are ranked and truncated.
func (c *Client) List(ctx context.Context, ttl time.Duration, refresh bool) (Listing, error) {
	asked := time.Now()
	if l, ok := c.cached(ttl, refresh, asked); ok {
		return l, nil
	}
	// One scan at a time. Whoever waited here re-checks the cache, so
	// concurrent requests share the scan that just finished.
	c.scanMu.Lock()
	defer c.scanMu.Unlock()
	if l, ok := c.cached(ttl, refresh, asked); ok {
		return l, nil
	}
	c.cacheMu.Lock()
	gen := c.cacheGen
	c.cacheMu.Unlock()
	l, complete, err := c.scan(ctx)
	if err != nil {
		return Listing{}, err
	}
	c.cacheMu.Lock()
	// A delete during the scan invalidated what was read; don't cache it.
	if c.cacheGen == gen {
		c.cache, c.cachedAt, c.cacheComplete = l, time.Now(), complete
	}
	c.cacheMu.Unlock()
	return l, nil
}

// cached returns the cached listing if it may be served. With refresh, only
// a listing made after asked (by a scan this request waited for) counts.
func (c *Client) cached(ttl time.Duration, refresh bool, asked time.Time) (Listing, bool) {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	if c.cache.Tags == nil {
		return Listing{}, false
	}
	if refresh {
		return c.cache, c.cachedAt.After(asked)
	}
	// An incomplete listing is kept briefly, so a struggling registry
	// isn't rescanned on every request, but retried well before ttl.
	keep := ttl
	if !c.cacheComplete {
		keep = min(ttl, failedListingTTL)
	}
	return c.cache, time.Since(c.cachedAt) < keep
}

// scan reads the registry. complete is false when a request failed in a
// way that may succeed on retry.
func (c *Client) scan(ctx context.Context) (Listing, bool, error) {
	repos, more, err := c.repositories(ctx)
	if err != nil {
		return Listing{}, false, err
	}
	var problems []string
	if more {
		problems = append(problems, fmt.Sprintf("the registry has more than %d repositories; only the first %d are listed", c.cfg.MaxRepos, c.cfg.MaxRepos))
	}
	complete := true
	type job struct{ repo, tag string }
	var jobs []job
	for _, r := range repos {
		tags, err := c.Tags(ctx, r)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: could not list tags: %v", r, err))
			complete = false
			if len(tags) == 0 {
				continue
			}
		}
		if len(tags) > c.cfg.MaxScanTags {
			problems = append(problems, fmt.Sprintf("%s: %d tags; only the %d highest-versioned were checked for recency", r, len(tags), c.cfg.MaxScanTags))
			sort.Slice(tags, func(i, j int) bool { return versionLess(tags[j], tags[i]) })
			tags = tags[:c.cfg.MaxScanTags]
		}
		for _, t := range tags {
			jobs = append(jobs, job{r, t})
		}
	}
	described := make([]Tag, len(jobs))
	var failed atomic.Bool
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var err error
			described[i], err = c.describe(ctx, j.repo, j.tag)
			// A tag deleted since it was listed is permanent; anything else
			// (timeouts, 5xx, auth hiccups) may clear on the next try.
			if err != nil && !errors.Is(err, ErrNotFound) {
				failed.Store(true)
			}
		}()
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return Listing{}, false, err // the caller gave up; don't serve or cache a half-read listing
	}
	if failed.Load() {
		complete = false
		problems = append(problems, "some tags could not be read; they are marked unreadable and will be retried")
	}

	// Newest first within each repository (ties and unknown times fall back
	// to version order), then keep MaxTags per repository.
	sort.SliceStable(described, func(i, j int) bool {
		a, b := described[i], described[j]
		if a.Repository != b.Repository {
			return a.Repository < b.Repository
		}
		if !a.Created.Equal(b.Created) {
			return a.Created.After(b.Created)
		}
		return versionLess(b.Tag, a.Tag)
	})
	out := make([]Tag, 0, len(described))
	perRepo := map[string]int{}
	for _, t := range described {
		if perRepo[t.Repository] < c.cfg.MaxTags {
			out = append(out, t)
		}
		perRepo[t.Repository]++
	}
	return Listing{Tags: out, Problems: problems}, complete, nil
}

// DeleteTag deletes the manifest a tag points at. Every tag sharing that
// digest goes with it, and the registry must allow deletes
// (REGISTRY_STORAGE_DELETE_ENABLED=true on distribution).
func (c *Client) DeleteTag(ctx context.Context, repo, tag string) error {
	if !repoNameRe.MatchString(repo) || !tagRe.MatchString(tag) {
		return fmt.Errorf("%w: %q:%q", ErrInvalidName, repo, tag)
	}
	resp, err := c.request(ctx, http.MethodHead, "/v2/"+repo+"/manifests/"+tag, "repository:"+repo+":pull,delete", http.Header{"Accept": {manifestAccept}})
	if err != nil {
		return err
	}
	resp.Body.Close()
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		return fmt.Errorf("registry: no digest for %s:%s", repo, tag)
	}
	resp, err = c.request(ctx, http.MethodDelete, "/v2/"+repo+"/manifests/"+digest, "repository:"+repo+":pull,delete", nil)
	if err != nil {
		return err
	}
	resp.Body.Close()
	c.cacheMu.Lock()
	c.cache = Listing{}
	c.cacheGen++
	c.cacheMu.Unlock()
	return nil
}

// ErrInvalidName is returned for a repository or tag name that isn't valid
// in the distribution spec, so it can't be spliced into a registry URL.
var ErrInvalidName = errors.New("registry: invalid repository or tag name")

// The distribution spec's grammar for repository names and tags.
var (
	repoNameRe = regexp.MustCompile(`^[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*$`)
	tagRe      = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,127}$`)
)
