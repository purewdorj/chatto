package config

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

// appleIdentifierPattern matches an Apple Developer Team ID or an APNs key ID.
var appleIdentifierPattern = regexp.MustCompile(`^[A-Z0-9]{10}$`)

// nativeAppIDPattern matches a reverse-DNS iOS bundle ID or an Android
// package name. Both use dot-separated segments of letters, digits,
// underscores, and hyphens.
var nativeAppIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)+$`)

// maxNativeAppIDLength is the longest app identifier that the configuration
// and the public registration API accept.
const maxNativeAppIDLength = 255

// validateNative returns the configuration errors of the enabled native push
// transports. It checks only the shape of the settings; the push sender reads
// and parses the key material at startup.
func (c *PushConfig) validateNative(webserverURL string) []string {
	var errs []string
	if c.NativeConfigured() && webserverURL == "" {
		errs = append(errs, "webserver.url is required when native push (push.apns or push.fcm) is enabled")
	}
	if c.APNs.Enabled {
		if !appleIdentifierPattern.MatchString(c.APNs.TeamID) {
			errs = append(errs, "push.apns.team_id must be the 10-character Apple Developer Team ID when push.apns is enabled")
		}
		if !appleIdentifierPattern.MatchString(c.APNs.KeyID) {
			errs = append(errs, "push.apns.key_id must be the 10-character APNs key ID when push.apns is enabled")
		}
		errs = append(errs, exactlyOneSecretSource("push.apns.private_key", c.APNs.PrivateKey, "push.apns.private_key_file", c.APNs.PrivateKeyFile)...)
		errs = append(errs, validateNativeAppIDs("push.apns.topics", c.APNs.Topics)...)
	}
	if c.FCM.Enabled {
		errs = append(errs, exactlyOneSecretSource("push.fcm.service_account_json", c.FCM.ServiceAccountJSON, "push.fcm.service_account_file", c.FCM.ServiceAccountFile)...)
		errs = append(errs, validateNativeAppIDs("push.fcm.app_ids", c.FCM.AppIDs)...)
	}
	return errs
}

func exactlyOneSecretSource(inlineName, inline, fileName, file string) []string {
	hasInline := strings.TrimSpace(inline) != ""
	hasFile := strings.TrimSpace(file) != ""
	switch {
	case hasInline && hasFile:
		return []string{fmt.Sprintf("set only one of %s and %s", inlineName, fileName)}
	case !hasInline && !hasFile:
		return []string{fmt.Sprintf("%s or %s is required", inlineName, fileName)}
	default:
		return nil
	}
}

func validateNativeAppIDs(name string, values []string) []string {
	if len(values) == 0 {
		return []string{name + " must list at least one app identifier"}
	}
	seen := make(map[string]bool, len(values))
	var errs []string
	for _, value := range values {
		if len(value) > maxNativeAppIDLength || !nativeAppIDPattern.MatchString(value) {
			errs = append(errs, fmt.Sprintf("%s contains an invalid app identifier %q", name, value))
			continue
		}
		if seen[value] {
			errs = append(errs, fmt.Sprintf("%s contains the duplicate app identifier %q", name, value))
		}
		seen[value] = true
	}
	return errs
}

// PrivateKeyPEM returns the APNs authentication key from the inline setting
// or from the configured file.
func (c *APNsConfig) PrivateKeyPEM() ([]byte, error) {
	if strings.TrimSpace(c.PrivateKey) != "" {
		return []byte(c.PrivateKey), nil
	}
	data, err := os.ReadFile(c.PrivateKeyFile)
	if err != nil {
		return nil, fmt.Errorf("read push.apns.private_key_file: %w", err)
	}
	return data, nil
}

// ServiceAccountKey returns the FCM service account JSON from the inline
// setting or from the configured file.
func (c *FCMConfig) ServiceAccountKey() ([]byte, error) {
	if strings.TrimSpace(c.ServiceAccountJSON) != "" {
		return []byte(c.ServiceAccountJSON), nil
	}
	data, err := os.ReadFile(c.ServiceAccountFile)
	if err != nil {
		return nil, fmt.Errorf("read push.fcm.service_account_file: %w", err)
	}
	return data, nil
}

// AllowsTopic reports whether bundleID is a configured APNs topic.
func (c *APNsConfig) AllowsTopic(bundleID string) bool {
	return slices.Contains(c.Topics, bundleID)
}

// AllowsAppID reports whether packageName is a configured FCM app ID.
func (c *FCMConfig) AllowsAppID(packageName string) bool {
	return slices.Contains(c.AppIDs, packageName)
}
