// Package config discovers and parses the kiro-scim .env file located next to
// the binary and produces a validated Config (endpoint, region, token).
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Environment variable / .env key names.
const (
	EndpointKey = "KIRO_SCIM_ENDPOINT"
	TokenKey    = "KIRO_SCIM_TOKEN"
)

// EnvFileName is the configuration file looked for next to the binary.
const EnvFileName = ".env"

// allowedRegions is the set of AWS regions in which Kiro's SCIM endpoint is
// available. The region is derived from the endpoint host and validated against
// this set.
var allowedRegions = map[string]bool{
	"us-east-1":    true,
	"eu-central-1": true,
}

// Config holds the validated runtime configuration.
//
// The bearer token is intentionally stored in an unexported field and exposed
// only through the Token method, so that it does not appear when a Config is
// formatted with fmt verbs such as %v, %+v, or %#v.
type Config struct {
	Endpoint string // normalized SCIM base URL (https, no trailing slash)
	Region   string // AWS region derived from the endpoint host
	token    string
}

// Token returns the SCIM bearer token.
func (c Config) Token() string { return c.token }

// String implements fmt.Stringer so that printing a Config with %v or %s never
// reveals the token. (An unexported field alone is not enough: fmt prints
// unexported fields for %v/%+v/%#v.)
func (c Config) String() string {
	return fmt.Sprintf("Config{Endpoint:%s Region:%s Token:[REDACTED]}", c.Endpoint, c.Region)
}

// GoString implements fmt.GoStringer so that the %#v verb also redacts the
// token.
func (c Config) GoString() string {
	return fmt.Sprintf("config.Config{Endpoint:%q, Region:%q, token:\"[REDACTED]\"}", c.Endpoint, c.Region)
}

// Load discovers the .env file next to the running executable, merges it under
// the process environment (process environment wins), validates the result, and
// returns a Config.
func Load() (Config, error) {
	dir, err := executableDir()
	if err != nil {
		return Config{}, err
	}
	return loadFromDir(dir, os.Getenv)
}

// loadFromDir reads the .env in dir (if present), applies getenv overrides, and
// validates. It is separated from Load so tests can supply a directory and a
// fake getenv.
func loadFromDir(dir string, getenv func(string) string) (Config, error) {
	fileVals := map[string]string{}
	envPath := filepath.Join(dir, EnvFileName)

	f, err := os.Open(envPath)
	switch {
	case err == nil:
		defer f.Close()
		fileVals, err = parseEnv(f)
		if err != nil {
			return Config{}, fmt.Errorf("reading %s: %w", envPath, err)
		}
	case errors.Is(err, os.ErrNotExist):
		// Missing .env is not fatal on its own; the values may come from the
		// process environment. A missing required value is reported below.
	default:
		return Config{}, fmt.Errorf("opening %s: %w", envPath, err)
	}

	endpoint := resolve(EndpointKey, fileVals, getenv)
	token := resolve(TokenKey, fileVals, getenv)

	var missing []string
	if endpoint == "" {
		missing = append(missing, EndpointKey)
	}
	if token == "" {
		missing = append(missing, TokenKey)
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf(
			"missing required configuration %s: set it in %s or in the environment",
			strings.Join(missing, " and "), envPath)
	}

	normalized, region, err := validateEndpoint(endpoint)
	if err != nil {
		return Config{}, err
	}

	return Config{Endpoint: normalized, Region: region, token: token}, nil
}

// resolve returns the process-environment value for key if set and non-empty,
// otherwise the value parsed from the .env file.
func resolve(key string, fileVals map[string]string, getenv func(string) string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fileVals[key]
}

// parseEnv parses a minimal .env format: blank lines and lines whose first
// non-space character is '#' are ignored; every other line must contain '=' and
// is split on the first '='. Keys and values are trimmed of surrounding
// whitespace, and a single pair of matching surrounding quotes (" or ') is
// stripped from the value.
func parseEnv(r io.Reader) (map[string]string, error) {
	vals := map[string]string{}
	sc := bufio.NewScanner(r)
	// Allow long token values well beyond bufio's default 64KiB line cap.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("line %d: expected KEY=VALUE", lineNo)
		}
		key := strings.TrimSpace(line[:eq])
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", lineNo)
		}
		vals[key] = unquote(strings.TrimSpace(line[eq+1:]))
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return vals, nil
}

// unquote strips a single pair of matching surrounding quotes from s.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// validateEndpoint checks that endpoint is an https URL whose host has the
// shape scim.<region>.amazonaws.com with an allowed region. It returns the
// normalized endpoint (trailing slash trimmed) and the region.
func validateEndpoint(endpoint string) (normalized, region string, err error) {
	u, perr := url.Parse(strings.TrimSpace(endpoint))
	if perr != nil {
		return "", "", fmt.Errorf("%s is not a valid URL: %w", EndpointKey, perr)
	}
	if u.Scheme != "https" {
		return "", "", fmt.Errorf(
			"%s must be an https URL (got scheme %q); refusing to send the token over an unencrypted connection",
			EndpointKey, u.Scheme)
	}

	region = regionFromHost(u.Hostname())
	if region == "" {
		return "", "", fmt.Errorf(
			"%s host %q is not a recognized Kiro SCIM endpoint (expected scim.<region>.amazonaws.com); allowed regions: us-east-1, eu-central-1",
			EndpointKey, u.Hostname())
	}
	if !allowedRegions[region] {
		return "", "", fmt.Errorf(
			"region %q (from %s) is not supported; Kiro SCIM is available only in us-east-1 and eu-central-1",
			region, EndpointKey)
	}

	u.Path = strings.TrimRight(u.Path, "/")
	return strings.TrimRight(u.String(), "/"), region, nil
}

// regionFromHost extracts <region> from a host of the form
// scim.<region>.amazonaws.com. It returns "" if the host does not match that
// shape.
func regionFromHost(host string) string {
	host = strings.ToLower(host)
	const prefix = "scim."
	const suffix = ".amazonaws.com"
	if !strings.HasPrefix(host, prefix) || !strings.HasSuffix(host, suffix) {
		return ""
	}
	region := host[len(prefix) : len(host)-len(suffix)]
	// The region must be a single, non-empty label (no extra dots).
	if region == "" || strings.Contains(region, ".") {
		return ""
	}
	return region
}

// executableDir returns the directory containing the running executable,
// resolving symlinks when possible. This is where the .env file is expected.
func executableDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("determining executable path: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}
