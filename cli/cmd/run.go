package cmd

import (
	"context"
	"errors"
	"fmt"
	"github.com/chattocorp/chatto/cli/internal/pb/chatto/core/notification/v1"
	"github.com/chattocorp/chatto/cli/internal/pb/chatto/core/runtime_state/v1"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/chattocorp/chatto/cli/internal/config"
	"github.com/chattocorp/chatto/cli/internal/core"
	"github.com/chattocorp/chatto/cli/internal/embedded_nats"
	"github.com/chattocorp/chatto/cli/internal/exporter"
	"github.com/chattocorp/chatto/cli/internal/http_server"
	"github.com/chattocorp/chatto/cli/internal/memlimit"
	evtv1 "github.com/chattocorp/chatto/cli/internal/pb/chatto/core/evt/v1"
	"github.com/chattocorp/chatto/cli/internal/push"
	"github.com/chattocorp/chatto/cli/internal/runtimeunit"
	searchbleve "github.com/chattocorp/chatto/cli/internal/search/bleve"
	"github.com/chattocorp/chatto/cli/internal/video"
	"github.com/chattocorp/chatto/pkg/natsruntime"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
	"golang.org/x/term"
)

// devStartupHook is called after core is initialized. Set by build-tagged init().
// Receives the loaded config so dev-only setup paths can read sections like
// `[bootstrap]` without a separate env-var or sidecar file. In bootstrap-tag
// builds this applies the [bootstrap] section from chatto.toml; in release
// builds this is a no-op.
var devStartupHook func(ctx context.Context, core *core.ChattoCore, cfg config.ChattoConfig)

const (
	optionalRuntimeUnitInitialRetry = time.Second
	optionalRuntimeUnitMaxRetry     = 30 * time.Second
)

func init() {
	gin.SetMode(gin.ReleaseMode)
}

var banner = `
   ::::::::  :::    :::     ::: ::::::::::: ::::::::::: ::::::::
  :+:    :+: :+:    :+:   :+: :+:   :+:         :+:    :+:    :+:
  +:+        +:+    +:+  +:+   +:+  +:+         +:+    +:+    +:+
  +#+        +#++:++#++ +#++:++#++: +#+         +#+    +#+    +:+
  +#+        +#+    +#+ +#+     +#+ +#+         +#+    +#+    +#+
  #+#    #+# #+#    #+# #+#     #+# #+#         #+#    #+#    #+#
   ########  ###    ### ###     ### ###         ###     ########
`

var configFile string

func runtimeUnitRegistrations() []runtimeunit.Registration {
	return []runtimeunit.Registration{
		{
			Unit: exporter.Unit{},
			StartWithRun: func(cfg config.ChattoConfig) bool {
				return cfg.Exporter.Enabled
			},
		},
		{
			Unit: searchbleve.Unit{},
			StartWithRun: func(cfg config.ChattoConfig) bool {
				return cfg.SearchProvider.Enabled
			},
		},
		{
			Unit: video.Unit{},
			StartWithRun: func(cfg config.ChattoConfig) bool {
				return cfg.AssetProcessing.Enabled
			},
		},
	}
}

var runCmd = &cobra.Command{
	Use:     "run",
	Aliases: []string{"start"},
	Short:   "Runs the chatto server",
	Run: func(cmd *cobra.Command, args []string) {
		runServer(configFile)
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
	runCmd.Flags().StringVarP(&configFile, "config", "c", "", "path to configuration file (default: chatto.toml)")
}

func runServer(configPath string) {
	startupStartedAt := time.Now()
	cfg, err := config.ReadConfig(configPath)
	if err != nil {
		log.Fatal("Failed to read configuration", "error", err)
	}

	configureLogging(cfg.General)
	if cfg.Video.MaxUploadSize > 0 {
		log.Warn("video.max_upload_size / CHATTO_VIDEO_MAX_UPLOAD_SIZE is deprecated; the larger upload limit now applies to all files. Set core.assets.max_upload_size / CHATTO_CORE_ASSETS_MAX_UPLOAD_SIZE and remove the video size setting")
	}
	log.Info("Starting Chatto", "version", Version)
	if limit, err := memlimit.FromContainer(); err != nil {
		log.Warn("Failed to set the Go memory limit from the container memory limit", "error", err)
	} else if limit > 0 {
		log.Info("Set the Go memory limit from the container memory limit", "go_memory_limit_bytes", limit)
	}
	if shouldPrintBanner(cfg.General.LogFormat, isLogOutputTerminal()) {
		printBanner()
	}

	stopStartupCPUProfile := startStartupCPUProfile(cfg.Diagnostics.StartupCPUProfile)
	startupCPUProfileStopped := false
	defer func() {
		if !startupCPUProfileStopped {
			stopStartupCPUProfile()
		}
	}()

	exitCode := 0
	defer func() {
		if exitCode != 0 {
			os.Exit(exitCode)
		}
	}()

	// Conductor stops foreground run scripts with SIGHUP before escalating.
	// Chatto has no reload-on-HUP behavior, so treat it as graceful shutdown
	// alongside the usual terminal and supervisor stop signals.
	shutdownSignals := runtimeunit.ShutdownSignals()
	signalLog := make(chan os.Signal, 1)
	stopSignalLog := make(chan struct{})
	signal.Notify(signalLog, shutdownSignals...)
	defer func() {
		signal.Stop(signalLog)
		close(stopSignalLog)
	}()
	go func() {
		select {
		case sig := <-signalLog:
			log.Info("Received shutdown signal", "signal", sig.String())
		case <-stopSignalLog:
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), shutdownSignals...)
	defer stop()

	// Use errgroup to coordinate services
	g, ctx := errgroup.WithContext(ctx)

	// Start embedded NATS if enabled (must be ready before other services)
	var embeddedNATS *natsruntime.Server
	if cfg.NATS.Embedded.Enabled {
		var err error
		embeddedNATS, err = embedded_nats.StartServer(&cfg.NATS.Embedded)
		if err != nil {
			log.Fatal("Failed to start embedded NATS server", "error", err)
		}
		defer embedded_nats.ShutdownServer(embeddedNATS)
	}

	// Connect to NATS
	nc, err := runtimeunit.ConnectToNATS(ctx, cfg, embeddedNATS)
	if err != nil {
		log.Error("Failed to connect to NATS", "error", err)
		exitCode = 1
		return
	}
	defer runtimeunit.CloseNATSConnection(nc)

	// Create Chatto core
	cfg.Core.AuthTokenTTL = cfg.Auth.TokenTTLOrDefault()
	cfg.Core.AuthProviders = cfg.Auth.Providers
	cfg.Core.AuthAccessTokenTTL = cfg.Auth.AccessTokenTTLOrDefault()
	cfg.Core.AuthLoopbackClientEnabled = cfg.Auth.LoopbackClientEnabled
	cfg.Core.EmailOTP = cfg.Auth.EmailOTP
	cfg.Core.Replicas = cfg.NATS.ReplicasOrDefault()
	cfg.Core.Limits = cfg.Limits
	cfg.Core.Owners = cfg.Owners
	cfg.Core.Version = Version
	cfg.Core.ServerOrigins = cfg.Webserver.ServerOrigins()
	chattoCore, err := core.NewChattoCore(ctx, nc, cfg.Core)
	if err != nil {
		log.Error("Failed to create Chatto core", "error", err)
		exitCode = 1
		return
	}
	chattoCore.VideoUploadsEnabled = cfg.Video.Enabled

	chattoCore.ConfigureLiveKitDiagnostics(cfg.LiveKit, cfg.Webserver.URL)
	if err := chattoCore.EnableLiveKitCallReconciliation(cfg.LiveKit); err != nil {
		log.Error("Failed to configure LiveKit call-state reconciliation", "error", err)
		exitCode = 1
		return
	}

	// Set up push notification callbacks if Web Push or native push is enabled
	if err := setupPushNotifications(chattoCore, cfg); err != nil {
		log.Error("Failed to configure push notifications", "error", err)
		exitCode = 1
		return
	}

	// Start core's background services (PresenceHub + projectors) BEFORE
	// bootstrap. Bootstrap triggers JoinRoom, which calls WaitForSeq on
	// the room-membership projector — if it's not running yet, the wait
	// blocks until the bootstrap context cancels.
	g.Go(func() error {
		return chattoCore.Run(ctx)
	})

	// Block until core.Run has finished its boot phase (projectors
	// started + ensureChannelRoomsAreInAGroup done). SeedDefaultRooms
	// issues CreateRoom calls whose default-group lookup hits the
	// RoomGroups projection — without this wait, the projection is
	// still empty and the seeded rooms land without a group.
	if err := chattoCore.WaitForBoot(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Error("Core boot never completed", "error", err)
		exitCode = 1
		return
	}
	stopStartupCPUProfile()
	startupCPUProfileStopped = true

	// Seed `announcements` + `general` on first boot of a fresh server.
	// Idempotent — no-op once any channel room exists.
	if err := chattoCore.SeedDefaultRooms(ctx); err != nil {
		if ctx.Err() != nil {
			return
		}
		log.Error("Failed to seed default rooms", "error", err)
		exitCode = 1
		return
	}

	// Run dev startup hook (auto-bootstrap in dev builds, no-op in prod)
	devStartupHook(ctx, chattoCore, cfg)
	if err := chattoCore.CompleteBootstrappedSetup(ctx); err != nil {
		log.Error("Failed to complete bootstrapped setup", "error", err)
		exitCode = 1
		return
	}

	unitRegistrations := runtimeUnitRegistrations()
	if err := runtimeunit.ValidateRegistrations(unitRegistrations); err != nil {
		log.Error("Failed to configure runtime units", "error", err)
		exitCode = 1
		return
	}
	for _, registration := range unitRegistrations {
		if !registration.Enabled(cfg) {
			continue
		}
		unit := registration.Unit
		env, err := runtimeunit.NewEnv(ctx, cfg, nc, log.WithPrefix(unit.Name()), Version)
		if err != nil {
			log.Error("Failed to create runtime unit environment", "unit", unit.Name(), "error", err)
			exitCode = 1
			return
		}
		g.Go(func() error {
			return runOptionalRuntimeUnit(ctx, env, unit)
		})
	}

	// Create and run HTTP server
	addr := net.JoinHostPort(cfg.Webserver.BindAddress, fmt.Sprint(cfg.Webserver.EffectivePort()))
	httpServer, err := http_server.NewHTTPServer(http_server.HTTPServerConfig{
		Config:           cfg,
		NC:               nc,
		Core:             chattoCore,
		Addr:             addr,
		Version:          Version,
		StartupStartedAt: startupStartedAt,
	})
	if err != nil {
		log.Error("Failed to create HTTP server", "error", err)
		exitCode = 1
		return
	}
	g.Go(func() error {
		return httpServer.Run(ctx)
	})

	// Wait for all services to complete (or one to fail)
	if err := g.Wait(); err != nil && err != context.Canceled {
		log.Error("Server failed", "error", err)
		exitCode = 1
	}
}

func runOptionalRuntimeUnit(ctx context.Context, env runtimeunit.Env, unit runtimeunit.Unit) error {
	return superviseOptionalRuntimeUnit(ctx, env, unit, optionalRuntimeUnitRetryDelay)
}

func superviseOptionalRuntimeUnit(
	ctx context.Context,
	env runtimeunit.Env,
	unit runtimeunit.Unit,
	retryDelay func(int) time.Duration,
) error {
	for attempt := 1; ; attempt++ {
		err := runtimeunit.Run(ctx, env, unit)
		if ctx.Err() != nil {
			return nil
		}
		delay := retryDelay(attempt)
		if err != nil {
			env.Logger.Error("Optional runtime unit stopped; restarting", "error", err, "restart_attempt", attempt, "retry_delay", delay)
		} else {
			env.Logger.Warn("Optional runtime unit stopped unexpectedly; restarting", "restart_attempt", attempt, "retry_delay", delay)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func optionalRuntimeUnitRetryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return optionalRuntimeUnitInitialRetry
	}
	delay := optionalRuntimeUnitInitialRetry
	for range attempt - 1 {
		if delay >= optionalRuntimeUnitMaxRetry/2 {
			return optionalRuntimeUnitMaxRetry
		}
		delay *= 2
	}
	return min(delay, optionalRuntimeUnitMaxRetry)
}

func printBanner() {
	for line := range strings.SplitSeq(banner, "\n") {
		log.Info(line)
	}
}

func configureLogging(cfg config.GeneralConfig) {
	setLogFormat(cfg.LogFormat, isLogOutputTerminal())
	setLogLevel(cfg.LogLevel)
}

func setLogFormat(format string, outputIsTerminal bool) {
	switch effectiveLogFormat(format, outputIsTerminal) {
	case "json":
		log.SetFormatter(log.JSONFormatter)
	case "logfmt":
		log.SetFormatter(log.LogfmtFormatter)
	default:
		log.SetFormatter(log.TextFormatter)
	}
}

func effectiveLogFormat(format string, outputIsTerminal bool) string {
	switch strings.ToLower(format) {
	case "", "auto":
		if outputIsTerminal {
			return "text"
		}
		return "json"
	case "json", "logfmt", "text":
		return strings.ToLower(format)
	default:
		return "text"
	}
}

func shouldPrintBanner(format string, outputIsTerminal bool) bool {
	return effectiveLogFormat(format, outputIsTerminal) == "text"
}

func isLogOutputTerminal() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

func setLogLevel(level string) {
	switch strings.ToLower(level) {
	case "debug":
		log.SetLevel(log.DebugLevel)
	case "info":
		log.SetLevel(log.InfoLevel)
	case "warn":
		log.SetLevel(log.WarnLevel)
	case "error":
		log.SetLevel(log.ErrorLevel)
	default:
		log.Warn("Unknown log level in configuration, defaulting to 'info'", "log_level", level)
		log.SetLevel(log.InfoLevel)
	}
}

// setupPushNotifications configures the push notification callbacks when
// Web Push, native push, or both are enabled. It returns an error when an
// enabled native provider has unusable key material.
func setupPushNotifications(chattoCore *core.ChattoCore, cfg config.ChattoConfig) error {
	logger := log.WithPrefix("push")
	var webSender notificationPushSender
	if cfg.Push.IsConfigured() {
		if sender := push.NewSender(cfg.Push, logger); sender != nil {
			webSender = sender
		}
	}
	var nativeSender nativePushSender
	native, err := push.NewNativeSender(cfg.Push, logger)
	if err != nil {
		return err
	}
	if native != nil {
		nativeSender = native
	}
	if webSender == nil && nativeSender == nil {
		return nil
	}

	logger.Info("Push notifications enabled",
		"web_push", webSender != nil,
		"apns", cfg.Push.APNs.Enabled,
		"fcm", cfg.Push.FCM.Enabled)

	chattoCore.OnPushTestRequested = pushTestHandler(chattoCore, cfg, webSender, nativeSender, logger)
	chattoCore.SetNotificationAlertHandler(notificationAlertHandler(chattoCore, cfg, webSender, nativeSender, logger))
	if nativeSender != nil {
		chattoCore.SetCallStartedHandler(incomingCallAlertHandler(chattoCore, cfg, nativeSender, logger))
		chattoCore.SetCallRingStoppedHandler(callRingStoppedHandler(chattoCore, cfg, nativeSender, logger))
	}
	return nil
}

// pushTestHandler sends a test notification to every current browser
// subscription and native alert device of a user. It skips APNs VoIP devices,
// which receive only call pushes. It succeeds when any device accepts the
// notification.
func pushTestHandler(chattoCore *core.ChattoCore, cfg config.ChattoConfig, webSender notificationPushSender, nativeSender nativePushSender, logger *log.Logger) func(context.Context, string) error {
	return func(ctx context.Context, userID string) error {
		var subscriptions []*runtimestatev1.PushSubscription
		if webSender != nil {
			current, err := chattoCore.GetUserPushSubscriptions(ctx, userID)
			if err != nil {
				return err
			}
			subscriptions, err = filterOwnedPushSubscriptions(ctx, chattoCore, userID, current)
			if err != nil {
				return fmt.Errorf("revalidate push endpoint ownership: %w", err)
			}
		}
		var devices []*runtimestatev1.NativePushDevice
		if nativeSender != nil {
			current, err := chattoCore.GetUserNativePushDevices(ctx, userID)
			if err != nil {
				return err
			}
			devices, err = filterCurrentNativePushDevices(ctx, chattoCore, userID, push.AlertDevices(current))
			if err != nil {
				return fmt.Errorf("revalidate native push device ownership: %w", err)
			}
		}
		if len(subscriptions) == 0 && len(devices) == 0 {
			return errors.New("no current push subscriptions registered")
		}

		outcome := deliverPush(ctx, chattoCore, userID, webSender, subscriptions, func(subscription *runtimestatev1.PushSubscription) *push.Payload {
			return &push.Payload{
				Title: "Test notification",
				Body:  "Push notifications are working.",
				URL:   push.NavigationBaseURL(subscription, cfg.Webserver.URL, cfg.Webserver.ServerOrigins()...),
				Icon:  "/icons/icon-192.png",
				Badge: "/icons/notification-badge.png",
				Tag:   "push-test",
			}
		}, nativeSender, devices, func(*runtimestatev1.NativePushDevice) *push.Payload {
			return push.BuildNativeTestPayload(cfg.Webserver.URL, userID)
		}, logger)
		if outcome.accepted {
			return nil
		}
		if outcome.err != nil {
			return outcome.err
		}
		return errors.New("push provider did not accept the test notification")
	}
}

// notificationPushSender delivers Web Push notifications.
type notificationPushSender interface {
	SendToManyMapped(context.Context, []*runtimestatev1.PushSubscription, func(*runtimestatev1.PushSubscription) *push.Payload) []*push.SendResult
}

// nativePushSender delivers native APNs and FCM notifications.
type nativePushSender interface {
	SendToMany(context.Context, []*runtimestatev1.NativePushDevice, func(*runtimestatev1.NativePushDevice) *push.Payload) []*push.NativeSendResult
}

// notificationAlertHandler keeps the production provider seam independently
// testable while ChattoCore owns durable signal consumption and terminal occurrence state.
// webSender or nativeSender is nil when that transport is not enabled.
func notificationAlertHandler(chattoCore *core.ChattoCore, cfg config.ChattoConfig, webSender notificationPushSender, nativeSender nativePushSender, logger *log.Logger) func(context.Context, *notificationv1.NotificationOccurrence) error {
	return func(ctx context.Context, occurrence *notificationv1.NotificationOccurrence) error {
		if core.NotificationOccurrenceHasUnsupportedSignal(occurrence) {
			return core.ErrUnsupportedNotificationSignal
		}
		if core.NotificationOccurrenceRoomID(occurrence) == "" {
			return core.ErrNotificationAlertSuppressed
		}
		recipientID := occurrence.GetRecipientId()
		var subscriptions []*runtimestatev1.PushSubscription
		if webSender != nil {
			var err error
			subscriptions, err = chattoCore.GetUserPushSubscriptions(ctx, recipientID)
			if err != nil {
				return fmt.Errorf("get push subscriptions: %w", err)
			}
		}
		var devices []*runtimestatev1.NativePushDevice
		if nativeSender != nil {
			var err error
			devices, err = chattoCore.GetUserNativePushDevices(ctx, recipientID)
			if err != nil {
				return fmt.Errorf("get native push devices: %w", err)
			}
			// APNs VoIP devices receive only call pushes.
			devices = push.AlertDevices(devices)
		}
		if len(subscriptions) == 0 && len(devices) == 0 {
			return core.ErrNotificationAlertSuppressed
		}
		actorName := "Someone"
		if occurrence.GetActorId() != "" {
			if actor, actorErr := chattoCore.GetUser(ctx, occurrence.GetActorId()); actorErr == nil && actor != nil {
				actorName = actor.DisplayName
				if actorName == "" {
					actorName = actor.Login
				}
			}
		}
		payloadCtx := fetchOccurrencePayloadContext(ctx, chattoCore, occurrence, logger)

		// Revalidate after hydration so a concurrent delete or visibility purge
		// cannot overtake a slow alert preparation.
		eligible, err := chattoCore.NotificationAlertEligible(ctx, occurrence)
		if err != nil {
			return err
		}
		if !eligible {
			return core.ErrNotificationAlertSuppressed
		}
		subscriptions, err = filterOwnedPushSubscriptions(ctx, chattoCore, recipientID, subscriptions)
		if err != nil {
			return fmt.Errorf("revalidate push endpoint ownership: %w", err)
		}
		devices, err = filterCurrentNativePushDevices(ctx, chattoCore, recipientID, devices)
		if err != nil {
			return fmt.Errorf("revalidate native push device ownership: %w", err)
		}
		if len(subscriptions) == 0 && len(devices) == 0 {
			return core.ErrNotificationAlertSuppressed
		}
		status, err := chattoCore.GetUserPresence(ctx, recipientID)
		if err != nil {
			return fmt.Errorf("revalidate notification presence before delivery: %w", err)
		}
		if status == core.PresenceStatusDoNotDisturb {
			return core.ErrNotificationAlertSuppressed
		}
		alertDeadline := core.NotificationAlertDeadline(occurrence)
		remaining := time.Until(alertDeadline)
		if remaining <= 0 {
			return core.ErrNotificationAlertSuppressed
		}
		sendCtx, cancel := context.WithDeadline(ctx, alertDeadline)
		defer cancel()
		outcome := deliverPush(sendCtx, chattoCore, recipientID, webSender, subscriptions, func(subscription *runtimestatev1.PushSubscription) *push.Payload {
			payload := push.BuildPayloadFromOccurrenceForSubscription(
				occurrence,
				actorName,
				cfg.Webserver.URL,
				subscription,
				payloadCtx,
				cfg.Webserver.ServerOrigins()...,
			)
			payload.DeliveryDeadline = alertDeadline
			return payload
		}, nativeSender, devices, func(*runtimestatev1.NativePushDevice) *push.Payload {
			payload := push.BuildPayloadFromOccurrence(occurrence, actorName, cfg.Webserver.URL, payloadCtx)
			payload.Kind = push.KindMessage
			payload.DeliveryDeadline = alertDeadline
			return payload
		}, logger)
		// Delivery is occurrence-scoped: once any current device accepts the
		// alert, complete delivery. Retrying the whole occurrence for another
		// failing endpoint would duplicate alerts on every successful device.
		if outcome.accepted {
			return nil
		}
		if outcome.err == nil {
			return core.ErrNotificationAlertSuppressed
		}
		return outcome.err
	}
}

// pushDeliveryOutcome combines the results of one Web Push and native fan-out.
type pushDeliveryOutcome struct {
	// accepted is true when at least one device accepted the push.
	accepted bool
	// err is the last transport error, or nil.
	err error
}

// deliverPush sends to the browser subscriptions and native devices of one
// user at the same time, and removes the devices that the providers report as
// gone. A nil sender or an empty device list skips that transport.
func deliverPush(
	ctx context.Context,
	chattoCore *core.ChattoCore,
	userID string,
	webSender notificationPushSender,
	subscriptions []*runtimestatev1.PushSubscription,
	webPayload func(*runtimestatev1.PushSubscription) *push.Payload,
	nativeSender nativePushSender,
	devices []*runtimestatev1.NativePushDevice,
	nativePayload func(*runtimestatev1.NativePushDevice) *push.Payload,
	logger *log.Logger,
) pushDeliveryOutcome {
	var webResults []*push.SendResult
	var nativeResults []*push.NativeSendResult
	var wg sync.WaitGroup
	if webSender != nil && len(subscriptions) > 0 {
		wg.Go(func() {
			webResults = webSender.SendToManyMapped(ctx, subscriptions, webPayload)
		})
	}
	if nativeSender != nil && len(devices) > 0 {
		wg.Go(func() {
			nativeResults = nativeSender.SendToMany(ctx, devices, nativePayload)
		})
	}
	wg.Wait()

	var outcome pushDeliveryOutcome
	// Gone-device cleanup must not depend on the delivery deadline.
	cleanupCtx := context.WithoutCancel(ctx)
	for _, result := range webResults {
		if result == nil {
			continue
		}
		if result.Gone {
			if err := chattoCore.DeletePushSubscription(cleanupCtx, userID, result.Endpoint); err != nil {
				logger.Warn("Failed to remove gone push subscription",
					"user_id", userID,
					"endpoint_hash", push.EndpointLogID(result.Endpoint),
					"error", err)
			}
			continue
		}
		if result.Success {
			outcome.accepted = true
			continue
		}
		if result.Error != nil {
			outcome.err = result.Error
		}
	}
	for _, result := range nativeResults {
		if result == nil {
			continue
		}
		if result.Gone {
			if err := chattoCore.DeleteNativePushDevice(cleanupCtx, userID, result.Token); err != nil {
				logger.Warn("Failed to remove gone native push device",
					"user_id", userID,
					"token_hash", push.EndpointLogID(result.Token),
					"error", err)
			} else {
				logger.Debug("Removed gone native push device",
					"user_id", userID,
					"token_hash", push.EndpointLogID(result.Token),
					"reason", result.Error)
			}
			continue
		}
		if result.Success {
			outcome.accepted = true
			continue
		}
		if result.Error != nil {
			outcome.err = result.Error
		}
	}
	return outcome
}

// filterCurrentNativePushDevices keeps the devices that are still the exact
// active records of userID.
func filterCurrentNativePushDevices(
	ctx context.Context,
	chattoCore *core.ChattoCore,
	userID string,
	devices []*runtimestatev1.NativePushDevice,
) ([]*runtimestatev1.NativePushDevice, error) {
	current := make([]*runtimestatev1.NativePushDevice, 0, len(devices))
	for _, device := range devices {
		ok, err := chattoCore.NativePushDeviceCurrentForUser(ctx, userID, device)
		if err != nil {
			return nil, err
		}
		if ok {
			current = append(current, device)
		}
	}
	return current, nil
}

// incomingCallAlertHandler sends an incoming-call native push to the other
// members of a direct message when a call starts there. push.CallRingDevices
// selects the devices. Before the push, it records the ring when a device can
// receive a call-end push, so callRingStoppedHandler can stop the ring later.
// It is best-effort: it logs failures and does not retry, because a ring is
// useful only for a short time.
func incomingCallAlertHandler(chattoCore *core.ChattoCore, cfg config.ChattoConfig, nativeSender nativePushSender, logger *log.Logger) core.CallStartedHandler {
	return func(ctx context.Context, roomID, callID, starterID string) {
		recipients, err := chattoCore.IncomingCallAlertRecipients(ctx, roomID, callID, starterID)
		if err != nil {
			logger.Warn("Failed to resolve incoming-call push recipients", "room_id", roomID, "error", err)
			return
		}
		if len(recipients) == 0 {
			return
		}
		callerName := "Someone"
		if starterID != "" {
			if caller, err := chattoCore.GetUser(ctx, starterID); err == nil && caller != nil {
				callerName = caller.DisplayName
				if callerName == "" {
					callerName = caller.Login
				}
			}
		}
		for _, recipientID := range recipients {
			devices, err := chattoCore.GetUserNativePushDevices(ctx, recipientID)
			if err != nil {
				logger.Warn("Failed to read native push devices for incoming call", "user_id", recipientID, "error", err)
				continue
			}
			devices, err = filterCurrentNativePushDevices(ctx, chattoCore, recipientID, push.CallRingDevices(devices))
			if err != nil {
				logger.Warn("Failed to revalidate native push devices for incoming call", "user_id", recipientID, "error", err)
				continue
			}
			if len(devices) == 0 {
				continue
			}
			if !chattoCore.IncomingCallStillRinging(roomID, callID, recipientID) {
				continue
			}
			if len(push.CallEndDevices(devices)) > 0 {
				if err := chattoCore.RecordCallRing(ctx, roomID, callID, recipientID); err != nil {
					// Ring anyway: a ring without a later call-end push stops by
					// its own timeout on the device.
					logger.Warn("Failed to record incoming-call ring", "user_id", recipientID, "room_id", roomID, "error", err)
				}
				// A join or a call end before the record did not find it. Check
				// again, and remove the record when nothing rings.
				if !chattoCore.IncomingCallStillRinging(roomID, callID, recipientID) {
					chattoCore.ReleaseCallRing(ctx, callID, recipientID)
					continue
				}
			}
			outcome := deliverPush(ctx, chattoCore, recipientID, nil, nil, nil, nativeSender, devices, func(*runtimestatev1.NativePushDevice) *push.Payload {
				return push.BuildIncomingCallPayload(cfg.Webserver.URL, roomID, callID, recipientID, callerName)
			}, logger)
			if !outcome.accepted && outcome.err != nil {
				logger.Debug("Incoming-call push was not accepted", "user_id", recipientID, "room_id", roomID, "error", outcome.err)
			}
		}
	}
}

// callRingStoppedHandler sends a call-end native push to the APNs VoIP and FCM
// devices of each account whose ring stopped, so those devices stop ringing.
// Plain APNs devices get nothing: an alert push cannot stop a ring. It is
// best-effort like incomingCallAlertHandler.
func callRingStoppedHandler(chattoCore *core.ChattoCore, cfg config.ChattoConfig, nativeSender nativePushSender, logger *log.Logger) core.CallRingStoppedHandler {
	return func(ctx context.Context, roomID, callID string, recipientIDs []string) {
		for _, recipientID := range recipientIDs {
			devices, err := chattoCore.GetUserNativePushDevices(ctx, recipientID)
			if err != nil {
				logger.Warn("Failed to read native push devices for call end", "user_id", recipientID, "error", err)
				continue
			}
			devices, err = filterCurrentNativePushDevices(ctx, chattoCore, recipientID, push.CallEndDevices(devices))
			if err != nil {
				logger.Warn("Failed to revalidate native push devices for call end", "user_id", recipientID, "error", err)
				continue
			}
			if len(devices) == 0 {
				continue
			}
			outcome := deliverPush(ctx, chattoCore, recipientID, nil, nil, nil, nativeSender, devices, func(*runtimestatev1.NativePushDevice) *push.Payload {
				return push.BuildCallEndPayload(cfg.Webserver.URL, roomID, callID, recipientID)
			}, logger)
			if !outcome.accepted && outcome.err != nil {
				logger.Debug("Call-end push was not accepted", "user_id", recipientID, "room_id", roomID, "error", outcome.err)
			}
		}
	}
}

func filterOwnedPushSubscriptions(
	ctx context.Context,
	chattoCore *core.ChattoCore,
	userID string,
	subscriptions []*runtimestatev1.PushSubscription,
) ([]*runtimestatev1.PushSubscription, error) {
	owned := make([]*runtimestatev1.PushSubscription, 0, len(subscriptions))
	for _, subscription := range subscriptions {
		isOwned, err := chattoCore.PushSubscriptionCurrentForUser(ctx, userID, subscription)
		if err != nil {
			return nil, err
		}
		if isOwned {
			owned = append(owned, subscription)
		}
	}
	return owned, nil
}

// fetchOccurrencePayloadContext builds a best-effort message preview and room
// name for an occurrence-backed push payload.
func fetchOccurrencePayloadContext(ctx context.Context, chattoCore *core.ChattoCore, occurrence *notificationv1.NotificationOccurrence, logger *log.Logger) *push.PayloadContext {
	if invitation := occurrence.GetSignal().GetRoomInvitationReceived(); invitation != nil {
		room, err := chattoCore.FindRoomByID(ctx, invitation.GetRoomId())
		if err != nil {
			return nil
		}
		return &push.PayloadContext{RoomName: room.GetName()}
	}
	target := core.NotificationOccurrenceMessageReference(occurrence)
	if target == nil {
		return nil
	}
	roomID := target.GetRoomId()
	eventID := target.GetEventId()

	if eventID == "" {
		return nil
	}

	payloadCtx := &push.PayloadContext{}

	kind, err := chattoCore.FindRoomKind(ctx, roomID)
	if err != nil {
		logger.Debug("Failed to resolve room kind for push notification preview",
			"room_id", roomID, "error", err)
		return nil
	}

	// Fetch the message to get its body
	event, err := chattoCore.GetRoomEventByEventID(ctx, kind, roomID, eventID)
	if err != nil {
		logger.Debug("Failed to fetch event for push notification preview",
			"event_id", eventID,
			"error", err)
		return nil
	}
	if event == nil {
		return nil
	}

	// Extract message body from the event
	if _, ok := event.Event.(*evtv1.Event_MessagePosted); ok {
		body, err := chattoCore.GetMessageBody(ctx, event.Id)
		if err != nil {
			logger.Debug("Failed to fetch message body for push notification preview",
				"event_id", event.Id,
				"error", err)
		} else {
			payloadCtx.MessagePreview = body
		}
	}

	if kind != core.KindDM {
		room, err := chattoCore.GetRoom(ctx, kind, roomID)
		if err != nil {
			logger.Debug("Failed to fetch room for push notification",
				"room_id", roomID,
				"error", err)
		} else if room != nil {
			payloadCtx.RoomName = room.Name
		}
	}

	return payloadCtx
}
