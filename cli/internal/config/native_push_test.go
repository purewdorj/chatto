package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validNativeBaseConfig() ChattoConfig {
	return ChattoConfig{
		Webserver: WebserverConfig{
			Port:                4000,
			URL:                 "https://chat.example",
			CookieSigningSecret: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		Core: CoreConfig{
			SecretKey: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
			Assets: AssetsConfig{
				SigningSecret: "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff",
			},
		},
	}
}

func validAPNs() APNsConfig {
	return APNsConfig{
		Enabled:        true,
		TeamID:         "TEAM123456",
		KeyID:          "KEY1234567",
		PrivateKeyFile: "/run/secrets/apns.p8",
		Topics:         []string{"mn.setunari.chatto.mobile"},
	}
}

func validFCM() FCMConfig {
	return FCMConfig{
		Enabled:            true,
		ServiceAccountFile: "/run/secrets/fcm.json",
		AppIDs:             []string{"mn.setunari.chatto.mobile"},
	}
}

func TestChattoConfig_Validate_NativePush(t *testing.T) {
	tests := []struct {
		name     string
		modify   func(*ChattoConfig)
		errorMsg string
	}{
		{name: "APNs without Web Push", modify: func(c *ChattoConfig) { c.Push.APNs = validAPNs() }},
		{name: "FCM without Web Push", modify: func(c *ChattoConfig) { c.Push.FCM = validFCM() }},
		{name: "disabled APNs ignores incomplete settings", modify: func(c *ChattoConfig) { c.Push.APNs.TeamID = "bad" }},
		{
			name:     "native push requires webserver URL",
			modify:   func(c *ChattoConfig) { c.Push.FCM = validFCM(); c.Webserver.URL = "" },
			errorMsg: "webserver.url is required when native push",
		},
		{
			name:     "APNs team ID shape",
			modify:   func(c *ChattoConfig) { c.Push.APNs = validAPNs(); c.Push.APNs.TeamID = "team" },
			errorMsg: "push.apns.team_id",
		},
		{
			name:     "APNs key ID shape",
			modify:   func(c *ChattoConfig) { c.Push.APNs = validAPNs(); c.Push.APNs.KeyID = "" },
			errorMsg: "push.apns.key_id",
		},
		{
			name:     "APNs key source missing",
			modify:   func(c *ChattoConfig) { c.Push.APNs = validAPNs(); c.Push.APNs.PrivateKeyFile = "" },
			errorMsg: "push.apns.private_key or push.apns.private_key_file is required",
		},
		{
			name: "APNs two key sources",
			modify: func(c *ChattoConfig) {
				c.Push.APNs = validAPNs()
				c.Push.APNs.PrivateKey = "-----BEGIN PRIVATE KEY-----"
			},
			errorMsg: "set only one of push.apns.private_key and push.apns.private_key_file",
		},
		{
			name:     "APNs topics required",
			modify:   func(c *ChattoConfig) { c.Push.APNs = validAPNs(); c.Push.APNs.Topics = nil },
			errorMsg: "push.apns.topics must list at least one app identifier",
		},
		{
			name:     "APNs invalid topic",
			modify:   func(c *ChattoConfig) { c.Push.APNs = validAPNs(); c.Push.APNs.Topics = []string{"not a bundle"} },
			errorMsg: "push.apns.topics contains an invalid app identifier",
		},
		{
			name: "FCM duplicate app ID",
			modify: func(c *ChattoConfig) {
				c.Push.FCM = validFCM()
				c.Push.FCM.AppIDs = []string{"mn.setunari.chatto.mobile", "mn.setunari.chatto.mobile"}
			},
			errorMsg: "push.fcm.app_ids contains the duplicate app identifier",
		},
		{
			name:     "FCM key source missing",
			modify:   func(c *ChattoConfig) { c.Push.FCM = validFCM(); c.Push.FCM.ServiceAccountFile = "" },
			errorMsg: "push.fcm.service_account_json or push.fcm.service_account_file is required",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validNativeBaseConfig()
			tt.modify(&cfg)
			err := cfg.Validate()
			if tt.errorMsg == "" {
				if err != nil {
					t.Fatalf("Validate() unexpected error = %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.errorMsg) {
				t.Fatalf("Validate() error = %v, want to contain %q", err, tt.errorMsg)
			}
		})
	}
}

func TestPushConfig_NativeDefaultsAndAllowLists(t *testing.T) {
	cfg := PushConfig{APNs: validAPNs(), FCM: validFCM()}
	if !cfg.NativeConfigured() || cfg.IsConfigured() {
		t.Fatal("native push must be configured independently of Web Push")
	}
	if !cfg.APNs.IncludePreviewsOrDefault() || !cfg.FCM.IncludePreviewsOrDefault() {
		t.Fatal("previews must default to true")
	}
	disabled := false
	cfg.APNs.IncludePreviews = &disabled
	if cfg.APNs.IncludePreviewsOrDefault() {
		t.Fatal("include_previews = false was ignored")
	}
	if !cfg.APNs.AllowsTopic("mn.setunari.chatto.mobile") || cfg.APNs.AllowsTopic("mn.setunari.chatto") {
		t.Fatal("APNs topic allow-list must match exactly")
	}
	if !cfg.FCM.AllowsAppID("mn.setunari.chatto.mobile") || cfg.FCM.AllowsAppID("MN.SETUNARI.CHATTO.MOBILE") {
		t.Fatal("FCM app ID allow-list must match exactly")
	}
}

func TestNativePushKeySources(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "apns.p8")
	if err := os.WriteFile(keyPath, []byte("file-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	apns := APNsConfig{PrivateKeyFile: keyPath}
	if key, err := apns.PrivateKeyPEM(); err != nil || string(key) != "file-key" {
		t.Fatalf("PrivateKeyPEM from file = (%q, %v)", key, err)
	}
	apns.PrivateKey = "inline-key"
	if key, err := apns.PrivateKeyPEM(); err != nil || string(key) != "inline-key" {
		t.Fatalf("PrivateKeyPEM inline = (%q, %v)", key, err)
	}
	fcm := FCMConfig{ServiceAccountFile: filepath.Join(dir, "missing.json")}
	if _, err := fcm.ServiceAccountKey(); err == nil {
		t.Fatal("ServiceAccountKey with a missing file returned no error")
	}
}

func TestReadConfig_NativePushFromEnv(t *testing.T) {
	tmpDir := t.TempDir()
	originalDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(originalDir) })

	t.Setenv("CHATTO_WEBSERVER_PORT", "4000")
	t.Setenv("CHATTO_WEBSERVER_URL", "https://chat.example")
	t.Setenv("CHATTO_WEBSERVER_COOKIE_SIGNING_SECRET", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	t.Setenv("CHATTO_CORE_SECRET_KEY", "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789")
	t.Setenv("CHATTO_CORE_ASSETS_SIGNING_SECRET", "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff")
	t.Setenv("CHATTO_PUSH_APNS_ENABLED", "true")
	t.Setenv("CHATTO_PUSH_APNS_TEAM_ID", "TEAM123456")
	t.Setenv("CHATTO_PUSH_APNS_KEY_ID", "KEY1234567")
	t.Setenv("CHATTO_PUSH_APNS_PRIVATE_KEY_FILE", "/run/secrets/apns.p8")
	t.Setenv("CHATTO_PUSH_APNS_TOPICS", "mn.setunari.chatto.mobile,mn.setunari.chatto.beta")
	t.Setenv("CHATTO_PUSH_APNS_INCLUDE_PREVIEWS", "false")
	t.Setenv("CHATTO_PUSH_FCM_ENABLED", "true")
	t.Setenv("CHATTO_PUSH_FCM_PROJECT_ID", "setunari")
	t.Setenv("CHATTO_PUSH_FCM_SERVICE_ACCOUNT_FILE", "/run/secrets/fcm.json")
	t.Setenv("CHATTO_PUSH_FCM_APP_IDS", "mn.setunari.chatto.mobile")

	cfg, err := ReadConfig("")
	if err != nil {
		t.Fatalf("ReadConfig() failed: %v", err)
	}
	if cfg.Push.Enabled {
		t.Fatal("Web Push must stay disabled")
	}
	apns := cfg.Push.APNs
	if !apns.Enabled || apns.TeamID != "TEAM123456" || apns.KeyID != "KEY1234567" || apns.PrivateKeyFile != "/run/secrets/apns.p8" {
		t.Fatalf("APNs config = %+v", apns)
	}
	if len(apns.Topics) != 2 || apns.Topics[1] != "mn.setunari.chatto.beta" || apns.IncludePreviewsOrDefault() {
		t.Fatalf("APNs topics/previews = %v / %v", apns.Topics, apns.IncludePreviewsOrDefault())
	}
	fcm := cfg.Push.FCM
	if !fcm.Enabled || fcm.ProjectID != "setunari" || fcm.ServiceAccountFile != "/run/secrets/fcm.json" || !fcm.AllowsAppID("mn.setunari.chatto.mobile") || !fcm.IncludePreviewsOrDefault() {
		t.Fatalf("FCM config = %+v", fcm)
	}
}
