package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	runtimestatev1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/runtime_state/v1"
)

const (
	testNativeAPNsToken    = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"
	testNativeAppID        = "mn.setunari.chatto.mobile"
	testNativeCleanupToken = "native-push-cleanup-token-0123456789abcdef"
)

func apnsRegistration(token string) NativePushDeviceRegistration {
	return NativePushDeviceRegistration{
		Platform:     runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS,
		Token:        token,
		AppID:        testNativeAppID,
		APNsSandbox:  true,
		DeviceName:   "iPhone",
		CleanupToken: testNativeCleanupToken,
	}
}

func TestNativePushDeviceKeysDoNotContainToken(t *testing.T) {
	core, _ := setupTestCore(t)
	key := core.nativePushDeviceKey("user-1", testNativeAPNsToken)
	ownerKey := core.nativePushDeviceOwnerKey(testNativeAPNsToken)
	if strings.Contains(key, testNativeAPNsToken) || strings.Contains(ownerKey, testNativeAPNsToken) {
		t.Fatalf("keys contain the device token: %q %q", key, ownerKey)
	}
	if !strings.HasPrefix(key, "native_push_device.user-1.") || len(strings.TrimPrefix(key, "native_push_device.user-1.")) != nativePushDeviceKeyHashLength {
		t.Fatalf("device key = %q", key)
	}
	if extractUserIDFromNativePushKey(key) != "user-1" {
		t.Fatalf("extractUserIDFromNativePushKey(%q) = %q", key, extractUserIDFromNativePushKey(key))
	}
	fullHash := strings.TrimPrefix(ownerKey, nativePushDeviceOwnerKeyPrefix)
	if len(fullHash) != sha256HexLength || nativePushDeviceKeyFromHash("user-1", fullHash) != key {
		t.Fatalf("owner key %q does not reconstruct device key %q", ownerKey, key)
	}
	if extractUserIDFromNativePushKey("push_subscription.user-1.abc") != "" {
		t.Fatal("Web Push key parsed as a native device key")
	}
}

func TestSaveNativePushDeviceValidation(t *testing.T) {
	core, _ := setupTestCore(t)
	ctx := context.Background()
	tests := []struct {
		name   string
		modify func(*NativePushDeviceRegistration)
	}{
		{"unspecified platform", func(r *NativePushDeviceRegistration) {
			r.Platform = runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_UNSPECIFIED
		}},
		{"APNs token is not hex", func(r *NativePushDeviceRegistration) { r.Token = strings.Repeat("z", 64) }},
		{"APNs token with path", func(r *NativePushDeviceRegistration) { r.Token = "../../3/device/" + testNativeAPNsToken }},
		{"FCM token with slash", func(r *NativePushDeviceRegistration) {
			r.Platform = runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM
			r.Token = "token/with/slash"
		}},
		{"empty token", func(r *NativePushDeviceRegistration) { r.Token = "" }},
		{"invalid app ID", func(r *NativePushDeviceRegistration) { r.AppID = "no-dots" }},
		{"long device name", func(r *NativePushDeviceRegistration) {
			r.DeviceName = strings.Repeat("a", MaxNativePushDeviceNameLength+1)
		}},
		{"short cleanup token", func(r *NativePushDeviceRegistration) { r.CleanupToken = "short" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registration := apnsRegistration(testNativeAPNsToken)
			tt.modify(&registration)
			if _, err := core.SaveNativePushDevice(ctx, "native-validation-user", registration); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("SaveNativePushDevice = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestSaveAndListNativePushDevices(t *testing.T) {
	core, _ := setupTestCore(t)
	ctx := context.Background()
	userID := "native-list-user"

	saved, err := core.SaveNativePushDevice(ctx, userID, apnsRegistration(testNativeAPNsToken))
	if err != nil {
		t.Fatalf("SaveNativePushDevice: %v", err)
	}
	if !saved.GetApnsSandbox() || saved.GetSavedAt() == nil || saved.GetDeviceName() != "iPhone" {
		t.Fatalf("saved device = %+v", saved)
	}
	fcm := NativePushDeviceRegistration{
		Platform:     runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM,
		Token:        "fcm:token_123-abc.def",
		AppID:        testNativeAppID,
		APNsSandbox:  true,
		CleanupToken: testNativeCleanupToken,
	}
	savedFCM, err := core.SaveNativePushDevice(ctx, userID, fcm)
	if err != nil {
		t.Fatalf("SaveNativePushDevice FCM: %v", err)
	}
	if savedFCM.GetApnsSandbox() {
		t.Fatal("FCM device stored the APNs sandbox flag")
	}

	devices, err := core.GetUserNativePushDevices(ctx, userID)
	if err != nil {
		t.Fatalf("GetUserNativePushDevices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %d, want 2", len(devices))
	}
	if subscriptions, err := core.GetUserPushSubscriptions(ctx, userID); err != nil || len(subscriptions) != 0 {
		t.Fatalf("Web Push subscriptions = (%d, %v), want native devices kept apart", len(subscriptions), err)
	}

	if err := core.DeleteNativePushDevice(ctx, userID, testNativeAPNsToken); err != nil {
		t.Fatalf("DeleteNativePushDevice: %v", err)
	}
	if err := core.DeleteNativePushDevice(ctx, userID, testNativeAPNsToken); err != nil {
		t.Fatalf("second DeleteNativePushDevice: %v", err)
	}
	requirePushKeyPresence(t, core, core.nativePushDeviceKey(userID, testNativeAPNsToken), false)
	requirePushKeyPresence(t, core, core.nativePushDeviceOwnerKey(testNativeAPNsToken), false)
	devices, err = core.GetUserNativePushDevices(ctx, userID)
	if err != nil || len(devices) != 1 || devices[0].GetToken() != fcm.Token {
		t.Fatalf("devices after delete = (%v, %v), want only the FCM device", devices, err)
	}
}

func TestNativePushDeviceLimit(t *testing.T) {
	core, _ := setupTestCore(t)
	ctx := context.Background()
	userID := "native-limit-user"
	for i := range MaxNativePushDevicesPerUser {
		if _, err := core.SaveNativePushDevice(ctx, userID, apnsRegistration(fmt.Sprintf("%064x", i+1))); err != nil {
			t.Fatalf("save device %d: %v", i, err)
		}
	}
	if _, err := core.SaveNativePushDevice(ctx, userID, apnsRegistration(fmt.Sprintf("%064x", 999))); !errors.Is(err, ErrNativePushDeviceLimitReached) {
		t.Fatalf("save beyond limit = %v, want ErrNativePushDeviceLimitReached", err)
	}
	// Refreshing an existing device stays allowed at the limit.
	if _, err := core.SaveNativePushDevice(ctx, userID, apnsRegistration(fmt.Sprintf("%064x", 1))); err != nil {
		t.Fatalf("refresh at limit: %v", err)
	}
}

func TestNativePushDeviceOwnershipTransfer(t *testing.T) {
	core, _ := setupTestCore(t)
	ctx := context.Background()
	alice, bob := "native-owner-alice", "native-owner-bob"

	aliceDevice, err := core.SaveNativePushDevice(ctx, alice, apnsRegistration(testNativeAPNsToken))
	if err != nil {
		t.Fatalf("save alice: %v", err)
	}
	if _, err := core.SaveNativePushDevice(ctx, bob, apnsRegistration(testNativeAPNsToken)); err != nil {
		t.Fatalf("save bob: %v", err)
	}
	if devices, err := core.GetUserNativePushDevices(ctx, alice); err != nil || len(devices) != 0 {
		t.Fatalf("alice devices after transfer = (%v, %v), want none", devices, err)
	}
	if current, err := core.NativePushDeviceCurrentForUser(ctx, alice, aliceDevice); err != nil || current {
		t.Fatalf("alice device current after transfer = (%v, %v), want false", current, err)
	}
	if devices, err := core.GetUserNativePushDevices(ctx, bob); err != nil || len(devices) != 1 {
		t.Fatalf("bob devices = (%v, %v), want one", devices, err)
	}

	// Alice's stale removal must not release Bob's claim.
	if err := core.DeleteNativePushDevice(ctx, alice, testNativeAPNsToken); err != nil {
		t.Fatalf("delete alice: %v", err)
	}
	if devices, err := core.GetUserNativePushDevices(ctx, bob); err != nil || len(devices) != 1 {
		t.Fatalf("bob devices after alice removal = (%v, %v), want one", devices, err)
	}
}

func TestNativePushDeviceExpiry(t *testing.T) {
	core, _ := setupTestCore(t)
	ctx := context.Background()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	setNow := setPushClock(core, start)
	userID := "native-expiry-user"

	saved, err := core.SaveNativePushDevice(ctx, userID, apnsRegistration(testNativeAPNsToken))
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	setNow(start.Add(PushSubscriptionLifetime - time.Second))
	if devices, err := core.GetUserNativePushDevices(ctx, userID); err != nil || len(devices) != 1 {
		t.Fatalf("devices before expiry = (%v, %v), want one", devices, err)
	}

	// A refresh extends the lifetime.
	refreshed, err := core.SaveNativePushDevice(ctx, userID, apnsRegistration(testNativeAPNsToken))
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	setNow(start.Add(PushSubscriptionLifetime + time.Hour))
	if current, err := core.NativePushDeviceCurrentForUser(ctx, userID, refreshed); err != nil || !current {
		t.Fatalf("refreshed device current = (%v, %v), want true", current, err)
	}
	if current, err := core.NativePushDeviceCurrentForUser(ctx, userID, saved); err != nil || current {
		t.Fatalf("superseded device record current = (%v, %v), want false", current, err)
	}

	setNow(start.Add(2 * PushSubscriptionLifetime))
	if current, err := core.NativePushDeviceCurrentForUser(ctx, userID, refreshed); err != nil || current {
		t.Fatalf("expired device current = (%v, %v), want false", current, err)
	}
	if devices, err := core.GetUserNativePushDevices(ctx, userID); err != nil || len(devices) != 0 {
		t.Fatalf("devices after expiry = (%v, %v), want none", devices, err)
	}
	requirePushKeyPresence(t, core, core.nativePushDeviceKey(userID, testNativeAPNsToken), false)
	requirePushKeyPresence(t, core, core.nativePushDeviceOwnerKey(testNativeAPNsToken), false)
}

func TestDeleteNativePushDeviceByCapability(t *testing.T) {
	core, _ := setupTestCore(t)
	ctx := context.Background()
	alice, bob := "native-capability-alice", "native-capability-bob"
	if _, err := core.SaveNativePushDevice(ctx, alice, apnsRegistration(testNativeAPNsToken)); err != nil {
		t.Fatalf("save alice: %v", err)
	}
	bobRegistration := apnsRegistration(testNativeAPNsToken)
	bobRegistration.CleanupToken = "native-push-cleanup-token-bob-0123456789abcdef"
	if _, err := core.SaveNativePushDevice(ctx, bob, bobRegistration); err != nil {
		t.Fatalf("save bob: %v", err)
	}

	// Alice's stale cleanup token cannot remove Bob's registration.
	if err := core.DeleteNativePushDeviceByCapability(ctx, testNativeAPNsToken, testNativeCleanupToken); err != nil {
		t.Fatalf("stale capability cleanup: %v", err)
	}
	if devices, err := core.GetUserNativePushDevices(ctx, bob); err != nil || len(devices) != 1 {
		t.Fatalf("bob devices after stale cleanup = (%v, %v), want one", devices, err)
	}

	if err := core.DeleteNativePushDeviceByCapability(ctx, testNativeAPNsToken, bobRegistration.CleanupToken); err != nil {
		t.Fatalf("matching capability cleanup: %v", err)
	}
	if devices, err := core.GetUserNativePushDevices(ctx, bob); err != nil || len(devices) != 0 {
		t.Fatalf("bob devices after cleanup = (%v, %v), want none", devices, err)
	}
	if err := core.DeleteNativePushDeviceByCapability(ctx, testNativeAPNsToken, "short"); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("short cleanup token = %v, want ErrInvalidArgument", err)
	}
}

func TestDeleteUserRemovesNativePushDevices(t *testing.T) {
	chatto, _ := setupTestCore(t)
	ctx := context.Background()
	user, err := chatto.CreateUser(ctx, SystemActorID, "native-delete-user", "Native Delete User", "password123")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := chatto.SaveNativePushDevice(ctx, user.GetId(), apnsRegistration(testNativeAPNsToken)); err != nil {
		t.Fatalf("save device: %v", err)
	}
	if err := chatto.DeleteUser(ctx, SystemActorID, user.GetId()); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	requirePushKeyPresence(t, chatto, chatto.nativePushDeviceKey(user.GetId(), testNativeAPNsToken), false)
	requirePushKeyPresence(t, chatto, chatto.nativePushDeviceOwnerKey(testNativeAPNsToken), false)

	if _, err := chatto.SaveNativePushDevice(ctx, user.GetId(), apnsRegistration(testNativeAPNsToken)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("save after account deletion = %v, want ErrNotFound", err)
	}
}

func TestNativePushCleanupReconcilesLateWriteAndOrphanOwner(t *testing.T) {
	// Drive the reconciliation pass here; a running worker could remove the
	// late write first.
	chatto, _ := newTestCore(t)
	ctx := context.Background()
	userID := "native-late-write-user"
	appendPushAccountDeletionFact(t, chatto, userID)

	device := &runtimestatev1.NativePushDevice{
		Platform:     runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS,
		Token:        testNativeAPNsToken,
		AppId:        testNativeAppID,
		SavedAt:      timestamppb.New(time.Now()),
		CleanupToken: testNativeCleanupToken,
	}
	data, err := proto.Marshal(device)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := chatto.storage.runtimeStateKV.Put(ctx, chatto.nativePushDeviceKey(userID, testNativeAPNsToken), data); err != nil {
		t.Fatalf("store late device: %v", err)
	}
	if err := chatto.claimNativePushDeviceOwnership(ctx, userID, testNativeAPNsToken); err != nil {
		t.Fatalf("claim late device: %v", err)
	}
	// An owner record without its device, as after a crash between writes.
	orphanToken := strings.Repeat("ab", 32)
	orphanKey := chatto.nativePushDeviceOwnerKey(orphanToken)
	if _, err := chatto.storage.runtimeStateKV.Put(ctx, orphanKey, []byte(`{"user_id":"native-active-user","subscription_revision":7}`)); err != nil {
		t.Fatalf("store orphan owner: %v", err)
	}

	if err := chatto.pushSubscriptionCleanup.reconcileDeletedAccountPushState(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	requirePushKeyPresence(t, chatto, chatto.nativePushDeviceKey(userID, testNativeAPNsToken), false)
	requirePushKeyPresence(t, chatto, chatto.nativePushDeviceOwnerKey(testNativeAPNsToken), false)
	requirePushKeyPresence(t, chatto, orphanKey, false)
}

func TestGetUserNativePushDevicesPropagatesCorruptRecord(t *testing.T) {
	core, _ := setupTestCore(t)
	ctx := context.Background()
	userID := "native-corrupt-user"
	if _, err := core.storage.runtimeStateKV.Put(ctx, core.nativePushDeviceKey(userID, testNativeAPNsToken), []byte{0xff, 0xff}); err != nil {
		t.Fatal(err)
	}
	if _, err := core.GetUserNativePushDevices(ctx, userID); err == nil {
		t.Fatal("GetUserNativePushDevices with a corrupt record returned no error")
	}
	if _, err := core.DeleteAllUserPushSubscriptions(ctx, userID); err == nil {
		t.Fatal("DeleteAllUserPushSubscriptions must report the undecodable record")
	}
	if _, err := core.storage.runtimeStateKV.Get(ctx, core.nativePushDeviceKey(userID, testNativeAPNsToken)); !errors.Is(err, jetstream.ErrKeyNotFound) {
		t.Fatalf("undecodable record remains: %v", err)
	}
}
