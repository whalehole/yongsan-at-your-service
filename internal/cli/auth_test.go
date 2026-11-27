package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/yongsan/yongsan-at-your-service/internal/config"
)

type authTransport func(*http.Request) (*http.Response, error)

func (f authTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestConfiguredTokenAndEnvironmentFallback(t *testing.T) {
	t.Setenv("YAS_AUTH_TEST", "environment-token")
	for _, tc := range []struct{ name, token, want string }{
		{"config takes precedence", "configured-token", "configured-token"},
		{"empty config falls back", "", "environment-token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{GitLab: config.GitLabConfig{URL: "https://gitlab.example.com", Token: tc.token, TokenEnv: "YAS_AUTH_TEST"}}
			client, err := clientFor(cfg)
			if err != nil {
				t.Fatal(err)
			}
			client.HTTPClient = &http.Client{Transport: authTransport(func(r *http.Request) (*http.Response, error) {
				if r.Header.Get("PRIVATE-TOKEN") != tc.want {
					t.Error("wrong authentication source selected")
				}
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"commit":{"id":%q}}`, testSHA)))}, nil
			})}
			if _, err := client.ResolveBranch(context.Background(), "group/project", "main"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTokenEnvironmentFlagOverridesConfiguredToken(t *testing.T) {
	workingDirectory(t)
	writeConfig(t, "https://gitlab.example.com", "")
	data, err := os.ReadFile("yas.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), "token_env: YAS_TEST_TOKEN", "token: configured-token\n  token_env: YAS_TEST_TOKEN", 1))
	if err := os.WriteFile("yas.yaml", data, 0600); err != nil {
		t.Fatal(err)
	}
	a := &app{tokenEnv: "YAS_AUTH_TEST", format: "text", options: Options{ConfigPath: "yas.yaml"}}
	cmd := New(Options{})
	if err := cmd.PersistentFlags().Set("token-env", "YAS_AUTH_TEST"); err != nil {
		t.Fatal(err)
	}
	cfg, err := a.load(cmd, "tag", false)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitLab.Token != "" || cfg.GitLab.TokenEnv != "YAS_AUTH_TEST" {
		t.Fatal("explicit environment selection was not applied")
	}
}
