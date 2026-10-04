package appie

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func writeConfigFile(t *testing.T, path string, cfg config) {
	t.Helper()
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestSaveConfigAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigFile(t, path, config{AccessToken: "old", RefreshToken: "old-r"})
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	c := New(WithConfigPath(path), WithTokens("new", "new-r"))
	if err := c.saveConfig(); err != nil {
		t.Fatal(err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// A rename swaps in a new inode; an in-place write keeps the old one and
	// can leave a truncated file behind if the process dies mid-write.
	if os.SameFile(before, after) {
		t.Error("saveConfig wrote in place, want temp file plus rename")
	}
	if perm := after.Mode().Perm(); perm != 0600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want only config.json (temp file left behind?)", len(entries))
	}
	c2, err := NewWithConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c2.accessToken != "new" || c2.refreshToken != "new-r" {
		t.Errorf("reloaded tokens = %q/%q, want new/new-r", c2.accessToken, c2.refreshToken)
	}
}

// refreshServer rotates tokens like AH: each refresh token works once.
func refreshServer(t *testing.T, refreshes *atomic.Int32) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	valid := map[string]bool{"r0": true}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mobile-auth/v1/auth/token/refresh" {
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			ok := valid[body["refreshToken"]]
			delete(valid, body["refreshToken"])
			n := refreshes.Add(1)
			next := "r" + string(rune('0'+n))
			valid[next] = true
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(token{AccessToken: "a" + next[1:], RefreshToken: next, ExpiresIn: 3600})
			return
		}
		_, _ = w.Write([]byte(`{}`))
	}))
}

func TestRefreshWithinBufferBeforeExpiry(t *testing.T) {
	var refreshes atomic.Int32
	srv := refreshServer(t, &refreshes)
	defer srv.Close()

	c := New(WithBaseURL(srv.URL), WithTokens("a0", "r0"))
	c.expiresAt = time.Now().Add(10 * time.Second)

	if err := c.DoRequest(context.Background(), http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 1 {
		t.Errorf("refreshes = %d, want 1 for a token expiring within the buffer", refreshes.Load())
	}
}

func TestConcurrentRequestsRefreshOnce(t *testing.T) {
	var refreshes atomic.Int32
	srv := refreshServer(t, &refreshes)
	defer srv.Close()

	c := New(WithBaseURL(srv.URL), WithTokens("a0", "r0"))
	c.expiresAt = time.Now().Add(-time.Minute)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.DoRequest(context.Background(), http.MethodGet, "/x", nil, nil)
		}()
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Errorf("refreshes = %d, want 1 (a second refresh reuses the rotated token)", refreshes.Load())
	}
	if c.refreshToken != "r1" {
		t.Errorf("refresh token = %q, want r1", c.refreshToken)
	}
}

// Another process sharing the config file refreshed first. Refreshing again
// with our stale refresh token would fail, so the newer file tokens win.
func TestRefreshAdoptsNewerTokensFromConfigFile(t *testing.T) {
	var refreshes atomic.Int32
	srv := refreshServer(t, &refreshes)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, path, config{AccessToken: "a0", RefreshToken: "r0", ExpiresAt: time.Now().Add(10 * time.Second)})
	c, err := NewWithConfig(path, WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, path, config{AccessToken: "disk-a", RefreshToken: "disk-r", ExpiresAt: time.Now().Add(time.Hour)})

	if err := c.RefreshIfNeeded(context.Background()); err != nil {
		t.Fatal(err)
	}
	if refreshes.Load() != 0 {
		t.Errorf("refreshes = %d, want 0", refreshes.Load())
	}
	if c.accessToken != "disk-a" || c.refreshToken != "disk-r" {
		t.Errorf("tokens = %q/%q, want disk-a/disk-r", c.accessToken, c.refreshToken)
	}
}

func TestRefreshIfNeededPersistsAndReportsErrors(t *testing.T) {
	var refreshes atomic.Int32
	srv := refreshServer(t, &refreshes)
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, path, config{AccessToken: "a0", RefreshToken: "r0", ExpiresAt: time.Now().Add(-time.Minute)})
	c, err := NewWithConfig(path, WithBaseURL(srv.URL))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RefreshIfNeeded(context.Background()); err != nil {
		t.Fatal(err)
	}
	c2, err := NewWithConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c2.refreshToken != "r1" {
		t.Errorf("saved refresh token = %q, want r1", c2.refreshToken)
	}

	bad := New(WithBaseURL(srv.URL), WithTokens("x", "revoked"))
	bad.expiresAt = time.Now().Add(-time.Minute)
	if err := bad.RefreshIfNeeded(context.Background()); err == nil {
		t.Error("RefreshIfNeeded with a revoked refresh token returned nil, want error")
	}
}

func TestRetryOnceAfterUnauthorized(t *testing.T) {
	var refreshes, calls atomic.Int32
	var lastBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mobile-auth/v1/auth/token/refresh" {
			refreshes.Add(1)
			_ = json.NewEncoder(w).Encode(token{AccessToken: "fresh", RefreshToken: "r1", ExpiresIn: 3600})
			return
		}
		calls.Add(1)
		b, _ := io.ReadAll(r.Body)
		lastBody = string(b)
		if r.Header.Get("Authorization") != "Bearer fresh" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	// Expiry says the token is still valid, but AH revoked it anyway.
	c := New(WithBaseURL(srv.URL), WithTokens("revoked", "r0"))
	c.expiresAt = time.Now().Add(time.Hour)

	var out struct{ OK bool }
	if err := c.DoRequest(context.Background(), http.MethodPost, "/x", map[string]int{"n": 1}, &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || refreshes.Load() != 1 || calls.Load() != 2 {
		t.Errorf("ok=%v refreshes=%d calls=%d, want true/1/2", out.OK, refreshes.Load(), calls.Load())
	}
	if !strings.Contains(lastBody, `"n":1`) {
		t.Errorf("retried body = %q, want the original body resent", lastBody)
	}
}

func TestNoRetryLoopOnPersistentUnauthorized(t *testing.T) {
	var refreshes, calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/mobile-auth/v1/auth/token/refresh" {
			n := refreshes.Add(1)
			_ = json.NewEncoder(w).Encode(token{AccessToken: "a" + string(rune('0'+n)), RefreshToken: "r", ExpiresIn: 3600})
			return
		}
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(WithBaseURL(srv.URL), WithTokens("a0", "r0"))
	if err := c.DoRequest(context.Background(), http.MethodGet, "/x", nil, nil); err == nil {
		t.Fatal("want error on persistent 401")
	}
	if refreshes.Load() != 1 || calls.Load() != 2 {
		t.Errorf("refreshes=%d calls=%d, want 1/2", refreshes.Load(), calls.Load())
	}
}
