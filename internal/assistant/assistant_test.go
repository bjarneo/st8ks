package assistant

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeAPI answers the models endpoint and records the auth headers.
type fakeAPI struct {
	mu      sync.Mutex
	status  int
	key     string
	auth    string
	path    string
	counter int
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.key, f.auth, f.path = r.Header.Get("X-Api-Key"), r.Header.Get("Authorization"), r.URL.Path
	f.counter++
	status := f.status
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if status != http.StatusOK {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`))
		return
	}
	_, _ = w.Write([]byte(`{"type":"model","id":"claude-opus-5","display_name":"Claude Opus 5","created_at":"2026-01-01T00:00:00Z"}`))
}

func newAssistant(key string) *Assistant {
	return New(func(string, any) {}, func() string { return key }, func() string { return "claude-opus-5" })
}

func TestSettingsKeyIgnoresEnvironment(t *testing.T) {
	api := &fakeAPI{status: http.StatusOK}
	srv := httptest.NewServer(api)
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL + "/"
	defer func() { apiBase = old }()

	// A login shell can export a gateway URL and a second credential.
	t.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "stale-token")
	t.Setenv("ANTHROPIC_API_KEY", "env-key")

	msg, err := newAssistant("  sk-ant-api03-settings  ").Check()
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if msg != "The key works. Claude Opus 5 is available." {
		t.Errorf("message = %q", msg)
	}
	if api.key != "sk-ant-api03-settings" || api.auth != "" || api.path != "/v1/models/claude-opus-5" {
		t.Errorf("request: key %q, authorization %q, path %q", api.key, api.auth, api.path)
	}
}

func TestRejectedKeyShowsAPIMessage(t *testing.T) {
	api := &fakeAPI{status: http.StatusUnauthorized}
	srv := httptest.NewServer(api)
	defer srv.Close()
	old := apiBase
	apiBase = srv.URL + "/"
	defer func() { apiBase = old }()

	_, err := newAssistant("sk-ant-api03-bad").Check()
	want := "The Anthropic API did not accept the API key: authentication_error: invalid x-api-key. Check the key in Settings > Assistant."
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}

	// Without a key in the settings, the SDK uses the environment. The
	// message names a server that is not the Anthropic API.
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	t.Setenv("ANTHROPIC_API_KEY", "env-key")
	apiBase = old
	_, err = newAssistant("").Check()
	if err == nil || !strings.Contains(err.Error(), "did not accept the API key at 127.0.0.1:") {
		t.Errorf("error = %v, want the gateway host", err)
	}
	if api.key != "env-key" {
		t.Errorf("env key not used: %q", api.key)
	}
}

func TestCheckKey(t *testing.T) {
	for key, ok := range map[string]bool{
		"sk-ant-api03-abc":    true,
		"":                    true,
		"sk-ant-oat01-abc":    false,
		"sk-ant-admin01-abc":  false,
		"sk-ant-api03-a b":    false,
		`"sk-ant-api03-abc"`:  false,
		"sk-ant-api03-abc\nx": false,
	} {
		if err := CheckKey(key); (err == nil) != ok {
			t.Errorf("CheckKey(%q) = %v, want ok %v", key, err, ok)
		}
	}
}
