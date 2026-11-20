package devincli

import (
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/urfave/cli/v2"
)

const defaultBaseURL = "https://api.devin.ai"

type settings struct {
	APIKey            string
	BaseURL           string
	Timeout           time.Duration
	AllowInsecureHTTP bool
	ReadOnly          bool
	DryRun            bool
}

func resolveSettings(c *cli.Context, getenv func(string) string) (settings, error) {
	var result settings
	result.ReadOnly = lookupBool(c, "read-only")
	result.DryRun = lookupBool(c, "dry-run")
	result.AllowInsecureHTTP = lookupBool(c, "allow-insecure-http")

	result.BaseURL = settingValue(c, "base-url", "DEVIN_BASE_URL", getenv)
	if result.BaseURL == "" {
		result.BaseURL = defaultBaseURL
	}
	parsed, err := url.Parse(result.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") || parsed.Opaque != "" {
		return settings{}, fmt.Errorf("base URL must be an absolute origin")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && result.AllowInsecureHTTP) {
		return settings{}, fmt.Errorf("base URL must use HTTPS unless --allow-insecure-http is set")
	}
	parsed.Path = ""
	parsed.RawPath = ""
	result.BaseURL = strings.TrimRight(parsed.String(), "/")

	timeoutValue, timeoutSet := lookupString(c, "timeout")
	if !timeoutSet {
		timeoutValue = getenv("DEVIN_TIMEOUT")
	}
	if timeoutSet || timeoutValue != "" {
		result.Timeout, err = time.ParseDuration(timeoutValue)
		if err != nil {
			return settings{}, fmt.Errorf("timeout must be a Go duration")
		}
	}
	if result.Timeout < 0 {
		return settings{}, fmt.Errorf("timeout cannot be negative")
	}

	if !result.DryRun {
		result.APIKey = settingValue(c, "api-key", "DEVIN_API_KEY", getenv)
		if result.APIKey == "" {
			return settings{}, fmt.Errorf("API key is required")
		}
		for _, r := range result.APIKey {
			if unicode.IsControl(r) || unicode.IsSpace(r) {
				return settings{}, fmt.Errorf("API key contains invalid header characters")
			}
		}
	}
	return result, nil
}

func settingValue(c *cli.Context, flagName, envName string, getenv func(string) string) string {
	if value, ok := lookupString(c, flagName); ok {
		return value
	}
	return getenv(envName)
}

func lookupString(c *cli.Context, name string) (string, bool) {
	for _, current := range c.Lineage() {
		if current.IsSet(name) {
			return current.String(name), true
		}
	}
	return "", false
}

func lookupBool(c *cli.Context, name string) bool {
	for _, current := range c.Lineage() {
		if current.IsSet(name) {
			return current.Bool(name)
		}
	}
	return false
}
