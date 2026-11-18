package appie

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	defaultBaseURL       = "https://api.ah.nl"
	defaultUserAgent     = "Appie/9.28 (iPhone17,3; iPhone; CPU OS 26_1 like Mac OS X)"
	defaultClientID      = "appie-ios"
	defaultClientVersion = "9.28"

	// tokenRefreshBuffer refreshes the access token this long before it
	// expires, so a request never goes out with a token that lapses in flight.
	tokenRefreshBuffer = 60 * time.Second
)

// Client is the AH API client. It handles authentication, token management,
// and provides methods to interact with products, orders, shopping lists, and more.
//
// Client is safe for concurrent use. Token state is protected by a mutex.
type Client struct {
	httpClient    *http.Client
	baseURL       string
	userAgent     string
	clientID      string
	clientVersion string

	mu           sync.RWMutex
	accessToken  string
	refreshToken string
	memberID     string
	expiresAt    time.Time
	orderID      string // sent as appie-current-order-id header, mirroring the iOS app; the API may use this (not server-side state) to determine the active order
	orderHash    string

	// refreshMu serializes token refreshes. AH rotates the refresh token, so
	// two concurrent refreshes with the same token would leave one failing.
	refreshMu sync.Mutex

	configPath   string
	loginBaseURL string       // overridable for testing; defaults to "https://login.ah.nl"
	openBrowser  func(string) // overridable for testing; nil uses default
	logger       *log.Logger
}

// Option configures the client. Use With* functions to create options.
type Option func(*Client)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.httpClient = hc
	}
}

// WithBaseURL sets a custom base URL.
func WithBaseURL(url string) Option {
	return func(c *Client) {
		c.baseURL = url
	}
}

// WithClientID overrides the API client id (default "appie-ios").
// Use "appie-be-ios" for Albert Heijn Belgium.
func WithClientID(id string) Option {
	return func(c *Client) {
		c.clientID = id
	}
}

// WithLoginBaseURL overrides the OAuth login host (default "https://login.ah.nl").
// Use "https://login.ah.be" for Albert Heijn Belgium.
func WithLoginBaseURL(url string) Option {
	return func(c *Client) {
		c.loginBaseURL = url
	}
}

// WithTokens sets the access and refresh tokens.
func WithTokens(accessToken, refreshToken string) Option {
	return func(c *Client) {
		c.accessToken = accessToken
		c.refreshToken = refreshToken
	}
}

// WithLogger sets a logger for verbose request logging.
func WithLogger(l *log.Logger) Option {
	return func(c *Client) {
		c.logger = l
	}
}

// WithConfigPath sets the path to the config file.
func WithConfigPath(path string) Option {
	return func(c *Client) {
		c.configPath = path
	}
}

// New creates a new AH API client.
func New(opts ...Option) *Client {
	c := &Client{
		httpClient:    http.DefaultClient,
		baseURL:       defaultBaseURL,
		userAgent:     defaultUserAgent,
		clientID:      defaultClientID,
		clientVersion: defaultClientVersion,
		logger:        log.New(io.Discard, "", 0),
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// DefaultConfigPath returns the standard XDG config path for the appie config
// file ($XDG_CONFIG_HOME/appie/config.json, falling back to ~/.config/appie/config.json).
// If the user's home directory cannot be determined it returns ".appie.json" in cwd.
func DefaultConfigPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ".appie.json"
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "appie", "config.json")
}

// NewWithConfig creates a new client and loads config from the given path.
func NewWithConfig(configPath string, opts ...Option) (*Client, error) {
	c := New(append([]Option{WithConfigPath(configPath)}, opts...)...)

	if err := c.loadConfig(); err != nil {
		if os.IsNotExist(err) {
			return c, nil // Config doesn't exist yet, that's OK
		}
		return nil, err
	}

	return c, nil
}

// loadConfig loads the configuration from the config file.
func (c *Client) loadConfig() error {
	if c.configPath == "" {
		return fmt.Errorf("no config path set")
	}

	cfg, err := c.readConfig()
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.accessToken = cfg.AccessToken
	c.refreshToken = cfg.RefreshToken
	c.memberID = cfg.MemberID
	c.expiresAt = cfg.ExpiresAt
	c.mu.Unlock()

	return nil
}

// readConfig reads and parses the config file.
func (c *Client) readConfig() (config, error) {
	var cfg config
	data, err := os.ReadFile(c.configPath)
	if err != nil {
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("failed to parse config: %w", err)
	}
	return cfg, nil
}

// adoptNewerConfig loads the config file's tokens if they expire later than
// the in-memory ones. Another process sharing the file may have refreshed,
// which invalidates our refresh token.
func (c *Client) adoptNewerConfig() {
	if c.configPath == "" {
		return
	}
	cfg, err := c.readConfig()
	if err != nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cfg.RefreshToken == "" || !cfg.ExpiresAt.After(c.expiresAt) {
		return
	}
	c.accessToken = cfg.AccessToken
	c.refreshToken = cfg.RefreshToken
	c.expiresAt = cfg.ExpiresAt
	if cfg.MemberID != "" {
		c.memberID = cfg.MemberID
	}
}

// saveConfig writes the current configuration to the config file atomically
// (temp file in the same directory, then rename) with mode 0600.
func (c *Client) saveConfig() error {
	if c.configPath == "" {
		return fmt.Errorf("no config path set")
	}

	c.mu.RLock()
	cfg := config{
		AccessToken:  c.accessToken,
		RefreshToken: c.refreshToken,
		MemberID:     c.memberID,
		ExpiresAt:    c.expiresAt,
	}
	c.mu.RUnlock()

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}

	// CreateTemp opens the file with mode 0600.
	tmp, err := os.CreateTemp(filepath.Dir(c.configPath), filepath.Base(c.configPath)+".tmp-*")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), c.configPath); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return nil
}

// IsAuthenticated returns true if the client has an access token.
func (c *Client) IsAuthenticated() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.accessToken != ""
}

// setHeaders sets the common headers for API requests.
func (c *Client) setHeaders(req *http.Request) {
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("x-client-name", c.clientID)
	req.Header.Set("x-client-version", c.clientVersion)
	req.Header.Set("x-application", "AHWEBSHOP")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	c.mu.RLock()
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
	// The active-order header scopes the API to that order's delivery slot and
	// assortment. Product search must see the full catalog: when an order is
	// REOPENED it has no valid delivery context, so a scoped search returns zero
	// results (orderable=forDeliveryLocationOrSample). Skip the header for product
	// reads (search by query and by ids); keep it for order/basket operations that
	// genuinely need the active-order context.
	isProductRead := strings.Contains(req.URL.Path, "/product/search")
	if c.orderID != "" && !isProductRead {
		req.Header.Set("appie-current-order-id", c.orderID)
		if c.orderHash != "" {
			req.Header.Set("appie-current-order-hash", c.orderHash)
		}
	}
	c.mu.RUnlock()
}

// isAuthPath reports whether path is a token endpoint. These never trigger a
// refresh themselves, which would recurse.
func isAuthPath(path string) bool {
	return strings.HasPrefix(path, "/mobile-auth/")
}

// needsRefresh reports whether the access token expires within
// tokenRefreshBuffer and a refresh token is available.
func (c *Client) needsRefresh() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.refreshToken != "" && !c.expiresAt.IsZero() && time.Until(c.expiresAt) < tokenRefreshBuffer
}

// RefreshIfNeeded refreshes the access token if it expires within a minute
// and saves the rotated tokens to the config file. Requests call this
// automatically; callers only need it to surface refresh errors early.
func (c *Client) RefreshIfNeeded(ctx context.Context) error {
	if !c.needsRefresh() {
		return nil
	}
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	// Re-check under the lock: a concurrent caller or another process may
	// already have refreshed.
	c.adoptNewerConfig()
	if !c.needsRefresh() {
		return nil
	}
	return c.refreshAndSave(ctx)
}

// refreshAfterUnauthorized gets a new access token after the API rejected the
// rejected token. It is a no-op when the token already changed since the request.
// The caller must not hold refreshMu.
func (c *Client) refreshAfterUnauthorized(ctx context.Context, rejected string) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()
	c.adoptNewerConfig()
	c.mu.RLock()
	changed := c.accessToken != rejected
	c.mu.RUnlock()
	if changed {
		return nil
	}
	return c.refreshAndSave(ctx)
}

// refreshAndSave refreshes the tokens and persists them when a config path is set.
func (c *Client) refreshAndSave(ctx context.Context) error {
	if err := c.refreshAccessToken(ctx); err != nil {
		return err
	}
	if c.configPath == "" {
		return nil
	}
	return c.saveConfig()
}

// DoRequest performs an HTTP request and decodes the response. A 401 on a
// non-auth path triggers one token refresh and one retry.
func (c *Client) DoRequest(ctx context.Context, method, path string, body, result any) error {
	if !isAuthPath(path) {
		// Best effort: on failure the request goes out with the old token
		// and the API reports the error.
		_ = c.RefreshIfNeeded(ctx)
	}

	var data []byte
	if body != nil {
		var err error
		if data, err = json.Marshal(body); err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
	}

	status, respBody, sentToken, err := c.send(ctx, method, path, data)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized && sentToken != "" && !isAuthPath(path) {
		if c.refreshAfterUnauthorized(ctx, sentToken) == nil {
			if status, respBody, _, err = c.send(ctx, method, path, data); err != nil {
				return err
			}
		}
	}

	if status >= 400 {
		var apiErr apiError
		if json.Unmarshal(respBody, &apiErr) == nil && (apiErr.Code != "" || apiErr.Message != "") {
			return &apiErr
		}
		return fmt.Errorf("API error: %d %s", status, string(respBody))
	}

	if result != nil && len(respBody) > 0 {
		if err := json.Unmarshal(respBody, result); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
	}

	return nil
}

// send performs one HTTP round trip and returns the status, the body and
// the access token the request carried.
func (c *Client) send(ctx context.Context, method, path string, data []byte) (int, []byte, string, error) {
	var bodyReader io.Reader
	if data != nil {
		bodyReader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return 0, nil, "", fmt.Errorf("failed to create request: %w", err)
	}
	c.setHeaders(req)
	sentToken := strings.TrimPrefix(req.Header.Get("Authorization"), "Bearer ")

	start := time.Now()
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	c.logger.Printf("%s %s %d %s", method, path, resp.StatusCode, time.Since(start).Truncate(time.Millisecond))

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, "", fmt.Errorf("failed to read response: %w", err)
	}
	return resp.StatusCode, respBody, sentToken, nil
}

// DoGraphQL performs a GraphQL request.
func (c *Client) DoGraphQL(ctx context.Context, query string, variables map[string]any, result any) error {
	req := graphQLRequest{
		Query:     query,
		Variables: variables,
	}

	var resp graphQLResponse[json.RawMessage]
	if err := c.DoRequest(ctx, http.MethodPost, "/graphql", req, &resp); err != nil {
		return err
	}

	if len(resp.Errors) > 0 {
		return resp.Errors[0]
	}

	if result != nil {
		if err := json.Unmarshal(resp.Data, result); err != nil {
			return fmt.Errorf("failed to decode graphql response: %w", err)
		}
	}

	return nil
}
