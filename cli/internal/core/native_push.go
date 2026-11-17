package core

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	runtimestatev1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/runtime_state/v1"
	"github.com/chattocorp/chatto/pkg/jetstreamutil"
)

// ============================================================================
// Native Push Device Keys
// ============================================================================

const (
	nativePushDeviceKeyPrefix      = "native_push_device."
	nativePushDeviceOwnerKeyPrefix = "native_push_device_owner."
	// nativePushDeviceHashScope is the HMAC scope of device token hashes.
	nativePushDeviceHashScope = "native_push_device"
	// nativePushDeviceKeyHashLength is the number of hex characters of the
	// token HMAC in a device key. The owner key uses the complete HMAC.
	nativePushDeviceKeyHashLength = 32

	// MaxNativePushDevicesPerUser is the maximum number of active native
	// devices that one account can fan push delivery out to. It is separate
	// from the Web Push subscription limit.
	MaxNativePushDevicesPerUser = 16

	// MaxNativePushTokenLength is the longest accepted provider device token.
	MaxNativePushTokenLength = 4096
	// MaxNativePushAppIDLength is the longest accepted app identifier.
	MaxNativePushAppIDLength = 255
	// MaxNativePushDeviceNameLength is the longest accepted device name.
	MaxNativePushDeviceNameLength = 128
)

var (
	// ErrNativePushDeviceLimitReached is returned when a new device token would
	// exceed the account's active native device limit.
	ErrNativePushDeviceLimitReached = errors.New("native push device limit reached")

	// apnsDeviceTokenPattern matches an APNs device token in hex. Apple
	// documents the token as variable length; current tokens have 32 bytes.
	apnsDeviceTokenPattern = regexp.MustCompile(`^[0-9a-fA-F]{32,512}$`)
	// fcmDeviceTokenPattern matches the characters of an FCM registration
	// token. The pattern also keeps the token safe in a URL path and a key.
	fcmDeviceTokenPattern = regexp.MustCompile(`^[A-Za-z0-9_:.\-]+$`)
	// nativePushAppIDPattern matches an iOS bundle ID or Android package name.
	nativePushAppIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)+$`)
)

// nativePushTokenHash returns the HMAC of a device token. Keys contain only
// this hash, so a KV key listing does not reveal delivery credentials.
func (c *ChattoCore) nativePushTokenHash(token string) string {
	return c.runtimeTokenHash(nativePushDeviceHashScope, token)
}

// nativePushDeviceKey returns the KV key of one device of one account.
// Format: native_push_device.{userId}.{tokenHash[:32]}
func (c *ChattoCore) nativePushDeviceKey(userID, token string) string {
	return nativePushDeviceKeyFromHash(userID, c.nativePushTokenHash(token))
}

func nativePushDeviceKeyFromHash(userID, fullHash string) string {
	return nativePushDeviceKeyPrefix + userID + "." + fullHash[:nativePushDeviceKeyHashLength]
}

// nativePushDeviceKeyFilter returns the KV filter for all devices of a user.
func nativePushDeviceKeyFilter(userID string) string {
	return nativePushDeviceKeyPrefix + userID + ".*"
}

// nativePushDeviceOwnerKey returns the KV key that exclusively assigns a
// device token to the account that most recently registered it.
// Format: native_push_device_owner.{tokenHash}
func (c *ChattoCore) nativePushDeviceOwnerKey(token string) string {
	return nativePushDeviceOwnerKeyPrefix + c.nativePushTokenHash(token)
}

// extractUserIDFromNativePushKey extracts the user ID from a device key.
func extractUserIDFromNativePushKey(key string) string {
	parts := strings.Split(key, ".")
	if len(parts) != 3 || parts[0]+"." != nativePushDeviceKeyPrefix {
		return ""
	}
	return parts[1]
}

// ============================================================================
// Native Push Device Operations
// ============================================================================

// NativePushDeviceRegistration is the input of SaveNativePushDevice.
type NativePushDeviceRegistration struct {
	Platform     runtimestatev1.NativePushDevicePlatform
	Token        string
	AppID        string
	APNsSandbox  bool
	DeviceName   string
	CleanupToken string
}

// SaveNativePushDevice stores or refreshes a native push device of userID
// and makes the token deliver for only this account. Every save sets the save
// time, so the device stays deliverable for PushSubscriptionLifetime.
// Authorization: the caller verifies that userID is the authenticated user and
// that the server allows the platform and app identifier.
func (c *ChattoCore) SaveNativePushDevice(ctx context.Context, userID string, registration NativePushDeviceRegistration) (*runtimestatev1.NativePushDevice, error) {
	if err := validateNativePushDevice(registration); err != nil {
		return nil, err
	}
	if err := c.requirePushSubscriptionAccountActive(ctx, userID); err != nil {
		return nil, err
	}
	if err := c.checkNativePushDeviceCapacity(ctx, userID, registration.Token); err != nil {
		return nil, err
	}

	device := &runtimestatev1.NativePushDevice{
		Platform:     registration.Platform,
		Token:        registration.Token,
		AppId:        registration.AppID,
		ApnsSandbox:  registration.APNsSandbox && nativePushPlatformUsesAPNs(registration.Platform),
		DeviceName:   registration.DeviceName,
		SavedAt:      timestamppb.New(c.pushNow()),
		CleanupToken: registration.CleanupToken,
	}
	data, err := proto.Marshal(device)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal native push device: %w", err)
	}
	key := c.nativePushDeviceKey(userID, registration.Token)
	if _, err := c.storage.runtimeStateKV.Put(ctx, key, data); err != nil {
		return nil, fmt.Errorf("failed to store native push device: %w", err)
	}
	if err := c.claimNativePushDeviceOwnership(ctx, userID, registration.Token); err != nil {
		return nil, err
	}
	if err := c.requirePushSubscriptionAccountActive(ctx, userID); err != nil {
		_, cleanupErr := c.DeleteAllUserPushSubscriptions(ctx, userID)
		if cleanupErr != nil {
			return nil, errors.Join(err, fmt.Errorf("remove native push device written across account deletion: %w", cleanupErr))
		}
		return nil, err
	}

	c.logger.Debug("Native push device saved",
		"user_id", userID,
		"platform", registration.Platform.String(),
		"token_hash", c.nativePushTokenHash(registration.Token)[:nativePushDeviceKeyHashLength])
	return device, nil
}

// nativePushPlatformUsesAPNs reports whether platform delivers through APNs:
// alert pushes or VoIP pushes. Both use hex device tokens and the APNs
// sandbox flag.
func nativePushPlatformUsesAPNs(platform runtimestatev1.NativePushDevicePlatform) bool {
	return platform == runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS ||
		platform == runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP
}

func validateNativePushDevice(registration NativePushDeviceRegistration) error {
	switch registration.Platform {
	case runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS,
		runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_APNS_VOIP,
		runtimestatev1.NativePushDevicePlatform_NATIVE_PUSH_DEVICE_PLATFORM_FCM:
	default:
		return fmt.Errorf("%w: native push platform is required", ErrInvalidArgument)
	}
	if err := validateNativePushToken(registration.Platform, registration.Token); err != nil {
		return err
	}
	if len(registration.AppID) > MaxNativePushAppIDLength || !nativePushAppIDPattern.MatchString(registration.AppID) {
		return fmt.Errorf("%w: native push app ID must be a bundle ID or package name", ErrInvalidArgument)
	}
	if err := validateStringMaxLength("native push device name", registration.DeviceName, MaxNativePushDeviceNameLength); err != nil {
		return err
	}
	return validatePushCleanupToken(registration.CleanupToken)
}

func validateNativePushToken(platform runtimestatev1.NativePushDevicePlatform, token string) error {
	if token == "" {
		return fmt.Errorf("%w: native push token is required", ErrInvalidArgument)
	}
	if err := validateStringMaxLength("native push token", token, MaxNativePushTokenLength); err != nil {
		return err
	}
	switch {
	case nativePushPlatformUsesAPNs(platform):
		if !apnsDeviceTokenPattern.MatchString(token) {
			return fmt.Errorf("%w: APNs device token must be hexadecimal", ErrInvalidArgument)
		}
	default:
		if !fcmDeviceTokenPattern.MatchString(token) {
			return fmt.Errorf("%w: FCM registration token contains invalid characters", ErrInvalidArgument)
		}
	}
	return nil
}

func (c *ChattoCore) checkNativePushDeviceCapacity(ctx context.Context, userID, token string) error {
	devices, err := c.GetUserNativePushDevices(ctx, userID)
	if err != nil {
		return fmt.Errorf("failed to check native push device capacity: %w", err)
	}
	for _, device := range devices {
		if device.GetToken() == token {
			return nil
		}
	}
	if len(devices) >= MaxNativePushDevicesPerUser {
		return ErrNativePushDeviceLimitReached
	}
	return nil
}

func (c *ChattoCore) claimNativePushDeviceOwnership(ctx context.Context, userID, token string) error {
	return c.claimPushRecordOwnership(ctx, c.nativePushDeviceOwnerKey(token), c.nativePushDeviceKey(userID, token), userID, func(value []byte) (bool, error) {
		var current runtimestatev1.NativePushDevice
		if err := proto.Unmarshal(value, &current); err != nil {
			return false, fmt.Errorf("failed to unmarshal current native push device: %w", err)
		}
		return current.GetToken() == token, nil
	})
}

// nativePushDeviceExpired reports whether the most recent save of a device is
// older than PushSubscriptionLifetime. A device without a save time is
// expired.
func (c *ChattoCore) nativePushDeviceExpired(device *runtimestatev1.NativePushDevice) bool {
	savedAt := device.GetSavedAt()
	if savedAt == nil {
		return true
	}
	return !c.pushNow().Before(savedAt.AsTime().Add(PushSubscriptionLifetime))
}

// DeleteNativePushDevice removes a native push device of userID by token.
// Removing an unknown token succeeds.
// Authorization: the caller verifies that userID is the authenticated user.
func (c *ChattoCore) DeleteNativePushDevice(ctx context.Context, userID, token string) error {
	if err := validateStringMaxLength("native push token", token, MaxNativePushTokenLength); err != nil {
		return err
	}
	key := c.nativePushDeviceKey(userID, token)
	entry, err := c.storage.runtimeStateKV.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get native push device before deleting: %w", err)
	}
	if err := c.releasePushRecordOwnership(ctx, c.nativePushDeviceOwnerKey(token), userID, entry.Revision()); err != nil {
		return err
	}
	err = c.storage.runtimeStateKV.Delete(ctx, key, jetstream.LastRevision(entry.Revision()))
	if err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) && !jetstreamutil.IsSequenceConflict(err) {
		return fmt.Errorf("failed to delete native push device: %w", err)
	}
	c.logger.Debug("Native push device deleted", "user_id", userID)
	return nil
}

// DeleteNativePushDeviceByCapability removes only the exact current owner
// record of token whose per-save cleanup token matches. It is safe to call
// without an account session. A later save replaces the cleanup token, so a
// stale cleanup cannot remove the newer registration.
func (c *ChattoCore) DeleteNativePushDeviceByCapability(ctx context.Context, token, cleanupToken string) error {
	if token == "" {
		return fmt.Errorf("%w: native push token is required", ErrInvalidArgument)
	}
	if err := validateStringMaxLength("native push token", token, MaxNativePushTokenLength); err != nil {
		return err
	}
	if err := validatePushCleanupToken(cleanupToken); err != nil {
		return err
	}
	ownerKey := c.nativePushDeviceOwnerKey(token)
	owner, err := c.getPushRecordOwner(ctx, ownerKey)
	if err != nil || owner == nil {
		return err
	}
	key := c.nativePushDeviceKey(owner.UserID, token)
	entry, err := c.storage.runtimeStateKV.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get native push device for capability cleanup: %w", err)
	}
	if entry.Revision() != owner.SubscriptionRevision {
		return nil
	}
	var device runtimestatev1.NativePushDevice
	if err := proto.Unmarshal(entry.Value(), &device); err != nil {
		return fmt.Errorf("failed to unmarshal native push device for capability cleanup: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(device.GetToken()), []byte(token)) != 1 ||
		subtle.ConstantTimeCompare([]byte(device.GetCleanupToken()), []byte(cleanupToken)) != 1 {
		return nil
	}
	if err := c.releasePushRecordOwnership(ctx, ownerKey, owner.UserID, entry.Revision()); err != nil {
		return err
	}
	err = c.storage.runtimeStateKV.Delete(ctx, key, jetstream.LastRevision(entry.Revision()))
	if err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) && !jetstreamutil.IsSequenceConflict(err) {
		return fmt.Errorf("failed to delete native push device by capability: %w", err)
	}
	return nil
}

// GetUserNativePushDevices returns the current native push devices of userID:
// devices that are not expired and that own their token at their exact
// revision. It removes every expired device that it finds.
// Authorization: internal, or the caller verifies that userID is the
// authenticated user.
func (c *ChattoCore) GetUserNativePushDevices(ctx context.Context, userID string) ([]*runtimestatev1.NativePushDevice, error) {
	keys, err := listPushRuntimeStateKeys(ctx, c.storage.runtimeStateKV, nativePushDeviceKeyFilter(userID))
	if err != nil {
		return nil, fmt.Errorf("failed to list native push device keys: %w", err)
	}
	var devices []*runtimestatev1.NativePushDevice
	for _, key := range keys {
		entry, err := c.storage.runtimeStateKV.Get(ctx, key)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("failed to get native push device: %w", err)
		}
		var device runtimestatev1.NativePushDevice
		if err := proto.Unmarshal(entry.Value(), &device); err != nil {
			return nil, fmt.Errorf("failed to unmarshal native push device: %w", err)
		}
		ownerKey := c.nativePushDeviceOwnerKey(device.GetToken())
		if c.nativePushDeviceExpired(&device) {
			// Never deliver an expired device, even when its removal fails. A
			// later read retries the removal.
			if err := c.deleteNativePushDeviceRevision(ctx, userID, key, ownerKey, entry.Revision()); err != nil {
				c.logger.Warn("Failed to remove expired native push device", "user_id", userID, "error", err)
			}
			continue
		}
		owned, err := c.pushRecordRevisionOwnedByUser(ctx, ownerKey, userID, entry.Revision())
		if err != nil {
			return nil, err
		}
		if owned {
			devices = append(devices, &device)
		}
	}
	return devices, nil
}

// NativePushDeviceCurrentForUser reports whether device is still the exact
// active record of userID. Delivery checks this immediately before a send,
// because another account can take over the token in the meantime.
func (c *ChattoCore) NativePushDeviceCurrentForUser(ctx context.Context, userID string, device *runtimestatev1.NativePushDevice) (bool, error) {
	key := c.nativePushDeviceKey(userID, device.GetToken())
	entry, err := c.storage.runtimeStateKV.Get(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to get native push device: %w", err)
	}
	var current runtimestatev1.NativePushDevice
	if err := proto.Unmarshal(entry.Value(), &current); err != nil {
		return false, fmt.Errorf("failed to unmarshal native push device: %w", err)
	}
	if !proto.Equal(&current, device) || c.nativePushDeviceExpired(&current) {
		return false, nil
	}
	return c.pushRecordRevisionOwnedByUser(ctx, c.nativePushDeviceOwnerKey(device.GetToken()), userID, entry.Revision())
}

// deleteNativePushDeviceRevision releases the owner claim and then deletes the
// device record at the revision that the caller read. A revision conflict
// means that the app saved the device again, so the newer record stays.
func (c *ChattoCore) deleteNativePushDeviceRevision(ctx context.Context, userID, key, ownerKey string, revision uint64) error {
	if err := c.releasePushRecordOwnership(ctx, ownerKey, userID, revision); err != nil {
		return err
	}
	err := c.storage.runtimeStateKV.Delete(ctx, key, jetstream.LastRevision(revision))
	if err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) && !jetstreamutil.IsSequenceConflict(err) {
		return fmt.Errorf("failed to delete native push device: %w", err)
	}
	return nil
}

// deleteAllUserNativePushDevices removes every native push device of userID.
// DeleteAllUserPushSubscriptions calls it, so account deletion, its durable
// cleanup worker, and the reconciler also erase native devices.
func (c *ChattoCore) deleteAllUserNativePushDevices(ctx context.Context, userID string) (int, error) {
	keys, err := listPushRuntimeStateKeys(ctx, c.storage.runtimeStateKV, nativePushDeviceKeyFilter(userID))
	if err != nil {
		return 0, fmt.Errorf("failed to list native push device keys: %w", err)
	}
	deleted := 0
	var cleanupErrors []error
	for _, key := range keys {
		entry, err := c.storage.runtimeStateKV.Get(ctx, key)
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			continue
		}
		if err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("get native push device before deleting: %w", err))
			continue
		}
		var device runtimestatev1.NativePushDevice
		if err := proto.Unmarshal(entry.Value(), &device); err != nil {
			// Erase an undecodable record first; the reconciler removes its
			// orphaned owner record.
			deleteErr := c.storage.runtimeStateKV.Delete(ctx, key, jetstream.LastRevision(entry.Revision()))
			if deleteErr == nil || errors.Is(deleteErr, jetstream.ErrKeyNotFound) {
				deleted++
				cleanupErrors = append(cleanupErrors, fmt.Errorf("decode native push device during deletion: %w", err))
				continue
			}
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete undecodable native push device: %w", deleteErr))
			continue
		}
		if err := c.releasePushRecordOwnership(ctx, c.nativePushDeviceOwnerKey(device.GetToken()), userID, entry.Revision()); err != nil {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("release native push device owner: %w", err))
			continue
		}
		err = c.storage.runtimeStateKV.Delete(ctx, key, jetstream.LastRevision(entry.Revision()))
		if err != nil && !errors.Is(err, jetstream.ErrKeyNotFound) {
			// A revision conflict means that a concurrent registration wrote a
			// newer record. Report it so that the durable cleanup retries.
			cleanupErrors = append(cleanupErrors, fmt.Errorf("delete native push device: %w", err))
			continue
		}
		if err == nil {
			deleted++
		}
	}
	return deleted, errors.Join(cleanupErrors...)
}
