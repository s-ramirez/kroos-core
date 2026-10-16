package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ChatGPT subscription auth (Codex OAuth), ported from hermes-agent
// hermes_cli/auth_codex.py. Tokens live in ~/.kroos/auth.json under "chatgpt".
const (
	chatgptIssuer       = "https://auth.openai.com"
	chatgptClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	chatgptTokenURL     = chatgptIssuer + "/oauth/token"
	chatgptRefreshSkew  = 5 * time.Minute
	chatgptLoginTimeout = 15 * time.Minute
)

// ErrNotLoggedIn means no ChatGPT tokens are stored.
var ErrNotLoggedIn = errors.New("not logged in to ChatGPT; run `kroos login`")

type ChatGPTTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	IDToken      string    `json:"id_token,omitempty"`
	LastRefresh  time.Time `json:"last_refresh"`
}

// ChatGPTAuth hands out a fresh access token, refreshing and persisting as needed.
type ChatGPTAuth struct {
	path   string
	http   *http.Client
	mu     sync.Mutex
	tokens ChatGPTTokens
}

// DefaultAuthPath is ~/.kroos/auth.json.
func DefaultAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".kroos", "auth.json")
}

// LoadChatGPTAuth reads stored tokens; ErrNotLoggedIn if absent.
func LoadChatGPTAuth(path string) (*ChatGPTAuth, error) {
	store, err := readAuthStore(path)
	if err != nil {
		return nil, err
	}
	var t ChatGPTTokens
	if raw, ok := store["chatgpt"]; ok {
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	if t.AccessToken == "" && t.RefreshToken == "" {
		return nil, ErrNotLoggedIn
	}
	return &ChatGPTAuth{path: path, http: &http.Client{Timeout: 30 * time.Second}, tokens: t}, nil
}

// Token returns a valid access token, refreshing if it expires soon.
func (a *ChatGPTAuth) Token(ctx context.Context) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.tokens.AccessToken != "" && !jwtExpiresWithin(a.tokens.AccessToken, chatgptRefreshSkew) {
		return a.tokens.AccessToken, nil
	}
	if err := a.refreshLocked(ctx); err != nil {
		return "", err
	}
	return a.tokens.AccessToken, nil
}

// Refresh forces a token refresh (used after a 401).
func (a *ChatGPTAuth) Refresh(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.refreshLocked(ctx)
}

func (a *ChatGPTAuth) refreshLocked(ctx context.Context) error {
	if a.tokens.RefreshToken == "" {
		return ErrNotLoggedIn
	}
	var resp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {a.tokens.RefreshToken},
		"client_id":     {chatgptClientID},
	}
	status, err := postForm(ctx, a.http, chatgptTokenURL, form, &resp)
	if err != nil {
		return fmt.Errorf("chatgpt token refresh: %w", err)
	}
	if status == http.StatusBadRequest || status == http.StatusUnauthorized {
		return fmt.Errorf("chatgpt token refresh rejected (%d); run `kroos login`", status)
	}
	if status != http.StatusOK || resp.AccessToken == "" {
		return fmt.Errorf("chatgpt token refresh failed (%d)", status)
	}
	a.tokens.AccessToken = resp.AccessToken
	if resp.RefreshToken != "" {
		a.tokens.RefreshToken = resp.RefreshToken
	}
	if resp.IDToken != "" {
		a.tokens.IDToken = resp.IDToken
	}
	a.tokens.LastRefresh = time.Now().UTC()
	return saveChatGPTTokens(a.path, a.tokens)
}

// ChatGPTLogin runs the device-code flow and stores the tokens at path.
func ChatGPTLogin(ctx context.Context, path string, w io.Writer) error {
	hc := &http.Client{Timeout: 30 * time.Second}

	var dev struct {
		DeviceAuthID string `json:"device_auth_id"`
		UserCode     string `json:"user_code"`
		Interval     any    `json:"interval"`
	}
	status, err := postJSON(ctx, hc, chatgptIssuer+"/api/accounts/deviceauth/usercode",
		map[string]string{"client_id": chatgptClientID}, &dev)
	if err != nil {
		return fmt.Errorf("request device code: %w", err)
	}
	if status != http.StatusOK || dev.UserCode == "" || dev.DeviceAuthID == "" {
		return fmt.Errorf("request device code: status %d", status)
	}
	interval := 5
	switch v := dev.Interval.(type) {
	case float64:
		interval = int(v)
	case string:
		if n, err := strconv.Atoi(v); err == nil {
			interval = n
		}
	}
	interval = max(interval, 3)

	fmt.Fprintf(w, "1. Open %s/codex/device\n2. Enter code: %s\nWaiting for sign-in...\n", chatgptIssuer, dev.UserCode)

	var code struct {
		AuthorizationCode string `json:"authorization_code"`
		CodeVerifier      string `json:"code_verifier"`
	}
	deadline := time.Now().Add(chatgptLoginTimeout)
	for {
		if time.Now().After(deadline) {
			return errors.New("login timed out")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(interval) * time.Second):
		}
		status, err := postJSON(ctx, hc, chatgptIssuer+"/api/accounts/deviceauth/token",
			map[string]string{"device_auth_id": dev.DeviceAuthID, "user_code": dev.UserCode}, &code)
		if err != nil {
			continue // transient; keep polling until deadline
		}
		if status == http.StatusOK {
			break
		}
		if status != http.StatusForbidden && status != http.StatusNotFound {
			return fmt.Errorf("device auth poll: status %d", status)
		}
	}
	if code.AuthorizationCode == "" || code.CodeVerifier == "" {
		return errors.New("device auth response missing authorization code")
	}

	var tok struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
	}
	status, err = postForm(ctx, hc, chatgptTokenURL, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code.AuthorizationCode},
		"redirect_uri":  {chatgptIssuer + "/deviceauth/callback"},
		"client_id":     {chatgptClientID},
		"code_verifier": {code.CodeVerifier},
	}, &tok)
	if err != nil {
		return fmt.Errorf("token exchange: %w", err)
	}
	if status != http.StatusOK || tok.AccessToken == "" {
		return fmt.Errorf("token exchange: status %d", status)
	}
	return saveChatGPTTokens(path, ChatGPTTokens{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		IDToken:      tok.IDToken,
		LastRefresh:  time.Now().UTC(),
	})
}

// ChatGPTLogout removes stored ChatGPT tokens.
func ChatGPTLogout(path string) error {
	store, err := readAuthStore(path)
	if err != nil {
		return err
	}
	delete(store, "chatgpt")
	return writeAuthStore(path, store)
}

// chatgptAccountHeaders derives workspace headers from the access-token JWT.
func chatgptAccountHeaders(token string) map[string]string {
	h := map[string]string{}
	claims := jwtClaims(token)
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	if id, _ := auth["chatgpt_account_id"].(string); id != "" {
		h["ChatGPT-Account-ID"] = id
	}
	res, _ := auth["chatgpt_data_residency"].(string)
	if res == "" {
		res, _ = auth["chatgpt_compute_residency"].(string)
	}
	if res = strings.TrimSpace(res); res != "" {
		h["x-openai-internal-codex-residency"] = res
	}
	return h
}

func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	var claims map[string]any
	if json.Unmarshal(raw, &claims) != nil {
		return nil
	}
	return claims
}

func jwtExpiresWithin(token string, d time.Duration) bool {
	exp, ok := jwtClaims(token)["exp"].(float64)
	if !ok {
		return false // opaque token: rely on 401 → refresh
	}
	return time.Until(time.Unix(int64(exp), 0)) < d
}

func readAuthStore(path string) (map[string]json.RawMessage, error) {
	store := map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return store, nil
	}
	if err := json.Unmarshal(data, &store); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return store, nil
}

func writeAuthStore(path string, store map[string]json.RawMessage) error {
	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func saveChatGPTTokens(path string, t ChatGPTTokens) error {
	store, err := readAuthStore(path)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(t)
	if err != nil {
		return err
	}
	store["chatgpt"] = raw
	return writeAuthStore(path, store)
}

func postJSON(ctx context.Context, hc *http.Client, u string, body any, out any) (int, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	return doPost(ctx, hc, u, "application/json", bytes.NewReader(b), out)
}

func postForm(ctx context.Context, hc *http.Client, u string, form url.Values, out any) (int, error) {
	return doPost(ctx, hc, u, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), out)
}

// doPost decodes a 200 body into out; error bodies are dropped (may echo secrets).
func doPost(ctx context.Context, hc *http.Client, u, contentType string, body io.Reader, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, body)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}
