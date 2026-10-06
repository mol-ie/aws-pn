package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnv(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  map[string]string
	}{
		{
			name:  "simple pair",
			input: "KEY=value",
			want:  map[string]string{"KEY": "value"},
		},
		{
			name: "comments and blank lines ignored",
			input: "" +
				"# a comment\n" +
				"\n" +
				"   # indented comment\n" +
				"KEY=value\n",
			want: map[string]string{"KEY": "value"},
		},
		{
			name:  "surrounding whitespace trimmed",
			input: "  KEY  =  value  \n",
			want:  map[string]string{"KEY": "value"},
		},
		{
			name:  "double quotes stripped",
			input: `KEY="quoted value"`,
			want:  map[string]string{"KEY": "quoted value"},
		},
		{
			name:  "single quotes stripped",
			input: "KEY='quoted value'",
			want:  map[string]string{"KEY": "quoted value"},
		},
		{
			name:  "value may contain equals",
			input: "KEY=a=b=c",
			want:  map[string]string{"KEY": "a=b=c"},
		},
		{
			name:  "mismatched quotes left intact",
			input: `KEY="unterminated`,
			want:  map[string]string{"KEY": `"unterminated`},
		},
		{
			name:  "empty quoted value",
			input: `KEY=""`,
			want:  map[string]string{"KEY": ""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseEnv(strings.NewReader(tt.input))
			if err != nil {
				t.Fatalf("parseEnv returned error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("key %q: got %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestParseEnvErrors(t *testing.T) {
	for _, input := range []string{"no-equals-sign", "=novalue"} {
		if _, err := parseEnv(strings.NewReader(input)); err == nil {
			t.Errorf("parseEnv(%q) = nil error, want error", input)
		}
	}
}

func TestValidateEndpoint(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   string
		wantRegion string
		wantNorm   string
		wantErr    bool
	}{
		{
			name:       "valid us-east-1",
			endpoint:   "https://scim.us-east-1.amazonaws.com/tenant/scim/v2",
			wantRegion: "us-east-1",
			wantNorm:   "https://scim.us-east-1.amazonaws.com/tenant/scim/v2",
		},
		{
			name:       "valid eu-central-1 trailing slash normalized",
			endpoint:   "https://scim.eu-central-1.amazonaws.com/tenant/scim/v2/",
			wantRegion: "eu-central-1",
			wantNorm:   "https://scim.eu-central-1.amazonaws.com/tenant/scim/v2",
		},
		{
			name:     "http rejected",
			endpoint: "http://scim.us-east-1.amazonaws.com/tenant/scim/v2",
			wantErr:  true,
		},
		{
			name:     "disallowed region rejected",
			endpoint: "https://scim.us-west-2.amazonaws.com/tenant/scim/v2",
			wantErr:  true,
		},
		{
			name:     "non-scim host rejected",
			endpoint: "https://example.com/tenant/scim/v2",
			wantErr:  true,
		},
		{
			name:     "malformed host extra label rejected",
			endpoint: "https://scim.us.east.1.amazonaws.com/tenant/scim/v2",
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			norm, region, err := validateEndpoint(tt.endpoint)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got none (region=%q norm=%q)", region, norm)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if region != tt.wantRegion {
				t.Errorf("region: got %q, want %q", region, tt.wantRegion)
			}
			if norm != tt.wantNorm {
				t.Errorf("normalized: got %q, want %q", norm, tt.wantNorm)
			}
		})
	}
}

func TestLoadFromDir(t *testing.T) {
	const endpoint = "https://scim.us-east-1.amazonaws.com/tenant/scim/v2"

	writeEnv := func(t *testing.T, body string) string {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, EnvFileName), []byte(body), 0o600); err != nil {
			t.Fatalf("writing .env: %v", err)
		}
		return dir
	}
	noEnv := func(string) string { return "" }

	t.Run("loads from file", func(t *testing.T) {
		dir := writeEnv(t, fmt.Sprintf("%s=%s\n%s=secret-token\n", EndpointKey, endpoint, TokenKey))
		cfg, err := loadFromDir(dir, noEnv)
		if err != nil {
			t.Fatalf("loadFromDir: %v", err)
		}
		if cfg.Endpoint != endpoint {
			t.Errorf("endpoint: got %q, want %q", cfg.Endpoint, endpoint)
		}
		if cfg.Region != "us-east-1" {
			t.Errorf("region: got %q, want us-east-1", cfg.Region)
		}
		if cfg.Token() != "secret-token" {
			t.Errorf("token: got %q, want secret-token", cfg.Token())
		}
	})

	t.Run("process env overrides file", func(t *testing.T) {
		dir := writeEnv(t, fmt.Sprintf("%s=%s\n%s=file-token\n", EndpointKey, endpoint, TokenKey))
		getenv := func(k string) string {
			if k == TokenKey {
				return "env-token"
			}
			return ""
		}
		cfg, err := loadFromDir(dir, getenv)
		if err != nil {
			t.Fatalf("loadFromDir: %v", err)
		}
		if cfg.Token() != "env-token" {
			t.Errorf("token: got %q, want env-token (env should override file)", cfg.Token())
		}
	})

	t.Run("missing key reported", func(t *testing.T) {
		dir := writeEnv(t, fmt.Sprintf("%s=%s\n", EndpointKey, endpoint))
		_, err := loadFromDir(dir, noEnv)
		if err == nil {
			t.Fatal("expected error for missing token")
		}
		if !strings.Contains(err.Error(), TokenKey) {
			t.Errorf("error should name %s: %v", TokenKey, err)
		}
	})

	t.Run("missing file but env set", func(t *testing.T) {
		dir := t.TempDir() // no .env written
		getenv := func(k string) string {
			switch k {
			case EndpointKey:
				return endpoint
			case TokenKey:
				return "env-token"
			}
			return ""
		}
		cfg, err := loadFromDir(dir, getenv)
		if err != nil {
			t.Fatalf("loadFromDir: %v", err)
		}
		if cfg.Token() != "env-token" {
			t.Errorf("token: got %q, want env-token", cfg.Token())
		}
	})

	t.Run("missing file and no env reports path", func(t *testing.T) {
		dir := t.TempDir()
		_, err := loadFromDir(dir, noEnv)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), dir) {
			t.Errorf("error should mention the expected .env directory %q: %v", dir, err)
		}
	})
}

// TestTokenNotExposedByFormatting ensures the bearer token does not leak when a
// Config is printed with common fmt verbs.
func TestTokenNotExposedByFormatting(t *testing.T) {
	const secret = "super-secret-token-value"
	cfg := Config{
		Endpoint: "https://scim.us-east-1.amazonaws.com/tenant/scim/v2",
		Region:   "us-east-1",
		token:    secret,
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(verb, cfg)
		if strings.Contains(out, secret) {
			t.Errorf("formatting with %q exposed the token: %q", verb, out)
		}
	}
}
