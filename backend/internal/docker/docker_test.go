package docker

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func frame(stream byte, s string) []byte {
	h := make([]byte, 8)
	h[0] = stream
	binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
	return append(h, s...)
}

func readAll(t *testing.T, lr *LogReader) []LogLine {
	t.Helper()
	var out []LogLine
	for {
		l, err := lr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, l)
	}
}

func TestLogReaderMultiplexed(t *testing.T) {
	var b bytes.Buffer
	b.Write(frame(1, "hello\nwor"))
	b.Write(frame(2, "oops\r\n"))
	b.Write(frame(1, "ld\nno newline"))
	got := readAll(t, NewLogReader(&b, false))
	want := []LogLine{{"stdout", "hello"}, {"stderr", "oops"}, {"stdout", "world"}, {"stdout", "no newline"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestLogReaderTTY(t *testing.T) {
	got := readAll(t, NewLogReader(strings.NewReader("a\nb\r\nc"), true))
	if len(got) != 3 || got[0].Text != "a" || got[1].Text != "b" || got[2].Text != "c" {
		t.Fatalf("got %v", got)
	}
}

func TestSplitRef(t *testing.T) {
	cases := map[string][2]string{
		"nginx":                          {"nginx", "latest"},
		"nginx:1.27":                     {"nginx", "1.27"},
		"registry:5000/team/app":         {"registry:5000/team/app", "latest"},
		"registry:5000/team/app:v2":      {"registry:5000/team/app", "v2"},
		"app@sha256:abc":                 {"app", "sha256:abc"},
		"ghcr.io/org/app:2026.10.14-rc1": {"ghcr.io/org/app", "2026.10.14-rc1"},
	}
	for in, want := range cases {
		n, tag := splitRef(in)
		if n != want[0] || tag != want[1] {
			t.Errorf("splitRef(%q) = %q, %q; want %q, %q", in, n, tag, want[0], want[1])
		}
	}
}

func TestErrorsAndPullStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/missing/json":
			w.WriteHeader(404)
			json.NewEncoder(w).Encode(map[string]string{"message": "No such container: missing"})
		case "/images/create":
			// A pull can fail mid-stream with a 200 status.
			io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"error":"manifest unknown"}`+"\n")
		}
	}))
	defer srv.Close()
	c, err := New("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.InspectContainer(t.Context(), "missing")
	if !IsNotFound(err) || !strings.Contains(err.Error(), "No such container") {
		t.Fatalf("want not-found error, got %v", err)
	}
	if err := c.PullImage(t.Context(), "x:1", ""); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("want mid-stream pull error, got %v", err)
	}
}

func TestPruneVolumesByAPIVersion(t *testing.T) {
	for _, tc := range []struct {
		api        string
		all        bool
		wantFilter string
		wantErr    bool
	}{
		{"1.45", true, `{"all":["true"]}`, false},
		{"1.45", false, "", false},
		{"1.41", true, "", false},
		{"1.41", false, "", true},
		{"", true, `{"all":["true"]}`, false}, // unknown version: send the filter
	} {
		var gotFilter string
		versionCalls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/version":
				versionCalls++
				json.NewEncoder(w).Encode(map[string]string{"ApiVersion": tc.api})
			case "/volumes/prune":
				gotFilter = r.URL.Query().Get("filters")
				json.NewEncoder(w).Encode(map[string]any{})
			}
		}))
		c, _ := New("tcp://" + strings.TrimPrefix(srv.URL, "http://"))
		_, err := c.PruneVolumes(t.Context(), tc.all)
		c.PruneVolumes(t.Context(), tc.all)
		srv.Close()
		if versionCalls != 1 {
			t.Errorf("api %q: /version asked %d times, want once", tc.api, versionCalls)
		}
		if (err != nil) != tc.wantErr || gotFilter != tc.wantFilter {
			t.Errorf("api %s all=%v: filter %q err %v", tc.api, tc.all, gotFilter, err)
		}
	}
}
