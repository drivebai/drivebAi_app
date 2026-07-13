package main

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/drivebai/backend/internal/auth"
	"github.com/drivebai/backend/internal/config"
	"github.com/drivebai/backend/internal/database"
	"github.com/drivebai/backend/internal/email"
	"github.com/drivebai/backend/internal/handlers"
	"github.com/drivebai/backend/internal/middleware"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/push"
	"github.com/drivebai/backend/internal/repository"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/drivebai/backend/internal/urlsigner"
	"github.com/drivebai/backend/internal/ws"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

//go:embed static/*
var staticFiles embed.FS

func main() {
	// Setup structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Load config
	cfg, err := config.Load()
	if err != nil {
		logger.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	// Fail fast if production is missing critical env (Stripe keys, JWT
	// secret strength, URL signing secret, HTTPS base URL, etc.). In dev
	// this is a no-op so a fresh checkout still boots without setup.
	if err := cfg.ValidateForProduction(); err != nil {
		logger.Error("production env validation failed", "problems", err.Error())
		os.Exit(1)
	}

	if cfg.IsDevelopment() {
		logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))
	}

	// Connect to database
	ctx := context.Background()
	db, err := database.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	logger.Info("connected to database")

	// Initialize services
	jwtSvc := auth.NewJWTService(cfg.JWTSecret, cfg.JWTAccessTokenTTL, cfg.JWTRefreshTokenTTL)
	emailSvc := email.NewSender(email.SenderConfig{
		SendGridAPIKey:      cfg.SendGridAPIKey,
		SendGridFromEmail:   cfg.SendGridFromEmail,
		SendGridFromName:    cfg.SendGridFromName,
		MailerSendAPIKey:    cfg.MailerSendAPIKey,
		MailerSendFromEmail: cfg.MailerFromEmail,
		MailerSendFromName:  cfg.MailerFromName,
		DeeplinkScheme:      cfg.AppDeeplinkScheme,
		BaseURL:             cfg.AppBaseURL,
	}, logger)
	otpEmailSvc := email.NewOTPSender(cfg.MailerSendAPIKey, cfg.MailerFromEmail, cfg.MailerFromName, logger)

	// Initialize repositories
	userRepo := repository.NewUserRepository(db)
	tokenRepo := repository.NewTokenRepository(db)
	loginOTPRepo := repository.NewLoginOTPRepository(db)
	profileRepo := repository.NewProfileRepository(db)
	docRepo := repository.NewDocumentRepository(db)
	carRepo := repository.NewCarRepository(db)
	carPhotoRepo := repository.NewCarPhotoRepository(db)
	carDocRepo := repository.NewCarDocumentRepository(db)
	likesRepo := repository.NewLikesRepository(db)
	chatRepo := repository.NewChatRepository(db)
	leaseRepo := repository.NewLeaseRequestRepository(db)
	sharedDocsRepo := repository.NewSharedDocumentRepository(db)
	adminRepo := repository.NewAdminRepository(db)
	supportRepo := repository.NewSupportRepository(db)
	notifRepo := repository.NewNotificationRepository(db)
	deviceTokenRepo := repository.NewDeviceTokenRepository(db)
	onboardingRepo := repository.NewOnboardingProgressRepository(db)

	// Ensure uploads directory exists
	uploadDir := cfg.UploadDir
	if uploadDir == "" {
		uploadDir = "./uploads"
	}
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		logger.Error("failed to create uploads directory", "error", err)
		os.Exit(1)
	}

	// URL signer for private uploads. Production must set UPLOAD_URL_SECRET;
	// in development we fall back to the JWT secret so a fresh checkout boots.
	uploadSecret := cfg.UploadURLSecret
	if uploadSecret == "" {
		uploadSecret = cfg.JWTSecret
	}
	uploadSigner := urlsigner.New(uploadSecret)
	privateURLSigner := &handlers.PrivateURLSigner{Signer: uploadSigner, TTL: cfg.UploadURLTTL}
	logger.Info("upload url signing",
		"signer_configured", uploadSigner != nil,
		"require_signed_private", cfg.RequirePrivateUploadSignatures,
		"ttl", cfg.UploadURLTTL.String(),
	)

	// Initialize WebSocket hub
	wsHub := ws.NewHub(logger)
	go wsHub.Run()

	// Rate limiter for auth endpoints: 10 requests per minute per IP.
	// Internally starts a background cleanup goroutine.
	authRateLimiter := middleware.NewRateLimiter(10, time.Minute)
	defer authRateLimiter.Stop()

	// Initialize Stripe service
	stripeSvc := stripeService.NewService(cfg.StripeSecretKey, cfg.StripePublishableKey, cfg.StripeWebhookSecret, cfg.PlatformFeeBPS, logger)

	// Log Stripe configuration status (never log actual keys)
	logger.Info("stripe config",
		"secret_key_set", cfg.StripeSecretKey != "",
		"publishable_key_set", cfg.StripePublishableKey != "",
		"webhook_secret_set", cfg.StripeWebhookSecret != "",
		"platform_fee_bps", cfg.PlatformFeeBPS,
	)
	if cfg.StripeWebhookSecret == "" {
		logger.Warn("STRIPE_WEBHOOK_SECRET is empty — webhooks will fail signature verification")
	}

	// Initialize push notification service (nil if APNs not configured).
	// We don't fail startup on missing APNs — push degrades to in-app +
	// WebSocket only — but log a loud WARN so the silent-disable mode
	// that originally shipped to TestFlight without anyone noticing is
	// visible at every cold start until the secrets are wired.
	pushSvc := push.NewService(cfg.AppleTeamID, cfg.APNSKeyID, cfg.APNSAuthKeyP8, cfg.IOSBundleID, cfg.APNSSandbox, logger)
	if !cfg.HasPushConfigured() {
		logger.Warn("APNs not configured — push notifications will NOT be delivered (set APPLE_TEAM_ID, APNS_KEY_ID, APNS_AUTH_KEY_P8, IOS_BUNDLE_ID via fly secrets)")
	}

	// Initialize handlers
	authHandler := handlers.NewAuthHandler(userRepo, tokenRepo, profileRepo, jwtSvc, emailSvc, cfg, logger)
	otpAuthHandler := handlers.NewOTPAuthHandler(userRepo, tokenRepo, loginOTPRepo, profileRepo, jwtSvc, otpEmailSvc, logger)
	userHandler := handlers.NewUserHandler(userRepo, docRepo, profileRepo, tokenRepo, jwtSvc, uploadDir, logger)
	carHandler := handlers.NewCarHandler(carRepo, carPhotoRepo, carDocRepo, userRepo, uploadDir, privateURLSigner, cfg.MinWeeklyRentPrice, cfg.AutoApproveCars)
	// VIN decode gets ExistsByVIN so the wizard's Search step can show the
	// early "already listed on DrivaBai" signal (same definition of "in use"
	// as the create/update preflights).
	vinDecodeHandler := handlers.NewVINDecodeHandler(logger, carRepo.ExistsByVIN)
	likesHandler := handlers.NewLikesHandler(likesRepo, carRepo)
	chatHandler := handlers.NewChatHandler(chatRepo, uploadDir, wsHub, jwtSvc, privateURLSigner, logger)
	notifHandler := handlers.NewNotificationHandler(notifRepo, deviceTokenRepo, wsHub, pushSvc, logger)
	// Wire the central NotificationHandler into surfaces that emit pushes
	// on state changes the user should know about while backgrounded:
	// chat messages (gated by WS presence), support replies, admin profile
	// edits, accident submissions. Each handler keeps the notif handler
	// optional (setter wiring) so test constructors don't need to change.
	chatHandler.SetNotificationHandler(notifHandler)
	deviceTokenHandler := handlers.NewDeviceTokenHandler(deviceTokenRepo, logger)
	onboardingHandler := handlers.NewOnboardingHandler(onboardingRepo)
	keyHandoverRepo := repository.NewKeyHandoverRepository(db)
	pickupDeadline := time.Duration(cfg.PickupDeadlineMinutes) * time.Minute
	leaseHandler := handlers.NewLeaseRequestHandler(leaseRepo, carRepo, carDocRepo, userRepo, chatRepo, docRepo, sharedDocsRepo, keyHandoverRepo, stripeSvc, wsHub, notifHandler, privateURLSigner, pickupDeadline, logger)
	todayHandler := handlers.NewTodayHandler(leaseRepo, userRepo, logger)
	accidentRepo := repository.NewAccidentRepository(db)
	adminHandler := handlers.NewAdminHandler(adminRepo, userRepo, wsHub, privateURLSigner, logger)
	adminHandler.SetNotificationHandler(notifHandler)
	// Admin-triggered password reset (D7) reuses the exact ForgotPassword
	// internals: reset-token store + the transactional email sender.
	adminHandler.SetPasswordResetDependencies(tokenRepo, emailSvc)
	supportHandler := handlers.NewSupportHandler(supportRepo, adminRepo, wsHub, logger)
	// Push support replies to backgrounded users (setter owned by W1-C on
	// support.go). Mirrors the admin/accident/chat notif wiring above.
	supportHandler.SetNotificationHandler(notifHandler)
	accidentHandler := handlers.NewAccidentHandler(accidentRepo, adminRepo, wsHub, uploadDir, privateURLSigner, logger)
	accidentHandler.SetNotificationHandler(notifHandler)
	accidentHandler.SetChatRepository(chatRepo)
	keyHandoverHandler := handlers.NewKeyHandoverHandler(keyHandoverRepo, leaseRepo, carRepo, userRepo, wsHub, notifHandler, logger)
	vehicleReturnRepo := repository.NewVehicleReturnRepository(db)
	vehicleReturnHandler := handlers.NewVehicleReturnHandler(vehicleReturnRepo, leaseRepo, carRepo, userRepo, chatRepo, stripeSvc, wsHub, notifHandler, logger)

	// Purchase (buy the car) — mirrors the lease flow but with manual capture
	// held until buyer inspection accept. See DESIGN SPEC for the state
	// machine.
	purchaseRepo := repository.NewPurchaseRequestRepository(db)
	purchaseHandler := handlers.NewPurchaseRequestHandler(purchaseRepo, carRepo, userRepo, chatRepo, leaseRepo, stripeSvc, wsHub, notifHandler, privateURLSigner, uploadDir, logger)
	leaseHandler.SetPurchaseHandler(purchaseHandler)
	todayHandler.SetPurchaseRepository(purchaseRepo)

	// Setup router
	r := chi.NewRouter()

	// Global middleware
	r.Use(chiMiddleware.RequestID)
	r.Use(chiMiddleware.RealIP)
	r.Use(middleware.Logger(logger))
	r.Use(chiMiddleware.Recoverer)
	// CORS. We use token auth (Authorization: Bearer), not cookies, so
	// AllowCredentials stays false — this avoids the spec-illegal combo of
	// `*` origin + credentials that browsers reject. Allowlist is driven
	// by CORS_ALLOWED_ORIGINS (production validation enforces a real list).
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSAllowedOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-Request-ID"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))
	logger.Info("cors configured", "allowed_origins", cfg.CORSAllowedOrigins)

	// Health check
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		if err := db.Health(r.Context()); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("unhealthy"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// API routes
	r.Route("/api/v1", func(r chi.Router) {
		// Public listings endpoint (for drivers to browse available cars)
		r.Get("/listings", carHandler.ListAvailableListings)

		// Auth routes (public) — rate limited: 10 req/min per IP
		r.Route("/auth", func(r chi.Router) {
			r.Use(middleware.RateLimit(authRateLimiter))
			r.Post("/register", authHandler.Register)
			r.Post("/verify-email", authHandler.VerifyEmail)
			r.Post("/login", authHandler.Login)
			r.Post("/token/refresh", authHandler.RefreshToken)
			r.Post("/password/forgot", authHandler.ForgotPassword)
			r.Post("/password/reset", authHandler.ResetPassword)
			r.Post("/logout", authHandler.Logout)
			r.Post("/resend-otp", authHandler.ResendOTP)

			// Email-availability check (signup inline UX). Public, rate-limited
			// by the same middleware as everything else in /auth; privacy
			// posture matches /auth/otp/verify which already reveals account
			// existence via its kind discriminator.
			r.Post("/check-email", authHandler.CheckEmail)

			// OTP email login (passwordless)
			r.Post("/otp/request", otpAuthHandler.RequestOTP)
			r.Post("/otp/verify", otpAuthHandler.VerifyOTP)
			r.Post("/otp/complete-registration", otpAuthHandler.CompleteRegistration)
		})

		// WebSocket endpoint (auth via query param, not middleware)
		r.Get("/ws", chatHandler.HandleWebSocket)

		// Stripe webhook (no auth — verified via signature)
		r.Post("/stripe/webhook", leaseHandler.HandleWebhook)

		// Protected routes
		r.Group(func(r chi.Router) {
			r.Use(middleware.AuthMiddleware(jwtSvc))
			r.Get("/me", userHandler.GetCurrentUser)
			r.Patch("/profile", userHandler.UpdateProfile)

			// Mode profiles (Owner / Driver switch)
			r.Get("/me/profiles", userHandler.ListMyProfiles)
			r.Post("/me/profiles", userHandler.CreateMyProfile)
			r.Post("/me/active-profile", userHandler.SetActiveProfile)

			// Profile photo
			r.Post("/profile/photo", userHandler.UploadProfilePhoto)

			// Documents
			r.Get("/documents", userHandler.GetDocuments)
			r.Post("/documents/{type}", userHandler.UploadDocument)
			r.Delete("/documents/{id}", userHandler.DeleteDocument)

			// Onboarding (signup flow)
			r.Post("/onboarding/complete", userHandler.CompleteOnboarding)

			// Product-tour ("onboarding") progress. All three endpoints are
			// strictly self-scoped: the user id comes only from the JWT, so a
			// caller can read/write/reset ONLY their own rows. DELETE backs the
			// debug-build QA reset; it touches no product data.
			r.Get("/me/onboarding-progress", onboardingHandler.GetProgress)
			r.Put("/me/onboarding-progress", onboardingHandler.UpdateProgress)
			r.Delete("/me/onboarding-progress", onboardingHandler.ResetProgress)

			// Actions (Today tab) — chat requests
			r.Get("/me/actions", chatHandler.GetMyActions)

			// Today tab — lease request actions + seen marker
			r.Get("/today/actions", todayHandler.GetActions)
			r.Post("/today/actions/seen", todayHandler.MarkActionsSeen)

			// Notifications
			r.Get("/notifications", notifHandler.ListNotifications)
			r.Post("/notifications/{id}/read", notifHandler.MarkRead)
			r.Post("/notifications/read-all", notifHandler.MarkAllRead)

			// Device tokens (push notifications)
			r.Post("/me/device-token", deviceTokenHandler.RegisterDeviceToken)
			r.Delete("/me/device-token", deviceTokenHandler.DeleteDeviceToken)

			// Likes/Favorites
			r.Get("/me/likes", likesHandler.GetLikedListings)
			r.Post("/listings/{listingId}/like", likesHandler.LikeListing)
			r.Delete("/listings/{listingId}/like", likesHandler.UnlikeListing)

			// Cars
			r.Route("/cars", func(r chi.Router) {
				r.Get("/", carHandler.ListCars)
				r.Post("/", carHandler.CreateCar)
				// VIN decode (NHTSA proxy). Mounted under /cars/vin-decode to
				// stay grouped with the listing-flow endpoints; declared
				// BEFORE /{carId} so chi doesn't route "vin-decode" as a
				// car ID.
				r.Get("/vin-decode/{vin}", vinDecodeHandler.DecodeVIN)
				r.Get("/{carId}", carHandler.GetCar)
				r.Put("/{carId}", carHandler.UpdateCar)
				r.Delete("/{carId}", carHandler.DeleteCar)
				r.Post("/{carId}/pause", carHandler.PauseCar)
				r.Put("/{carId}/location", carHandler.UpdateCarLocation)

				// Car photos
				r.Get("/{carId}/photos", carHandler.ListCarPhotos)
				r.Post("/{carId}/photos", carHandler.UploadCarPhoto)
				r.Delete("/{carId}/photos/{photoId}", carHandler.DeleteCarPhoto)

				// Car documents
				r.Get("/{carId}/documents", carHandler.ListCarDocuments)
				r.Post("/{carId}/documents", carHandler.UploadCarDocument)
				r.Delete("/{carId}/documents/{docId}", carHandler.DeleteCarDocument)
			})

			// Chats
			r.Route("/chats", func(r chi.Router) {
				r.Get("/", chatHandler.ListChats)
				r.Post("/", chatHandler.FindOrCreateChat)
				r.Get("/{chatId}", chatHandler.GetChat)
				r.Get("/{chatId}/messages", chatHandler.ListMessages)
				r.Post("/{chatId}/messages", chatHandler.SendMessage)
				r.Post("/{chatId}/read", chatHandler.MarkRead)
				r.Get("/{chatId}/requests", chatHandler.ListRequests)
				r.Post("/{chatId}/requests", chatHandler.CreateRequest)
				r.Post("/{chatId}/requests/{requestId}/respond", chatHandler.RespondToRequest)
				r.Get("/{chatId}/details", chatHandler.GetChatDetails)
				r.Patch("/{chatId}/settings", chatHandler.UpdateSettings)
				r.Post("/{chatId}/archive", chatHandler.ArchiveChat)
				r.Get("/{chatId}/attachments", chatHandler.ListAttachments)
				r.Post("/{chatId}/attachments", chatHandler.UploadAttachment)
			})

			// User profile (for counterparty profiles in chat)
			r.Get("/users/{userId}/profile", chatHandler.GetUserProfile)

			// Lease requests
			r.Post("/listings/{listingId}/lease-requests", leaseHandler.CreateLeaseRequest)
			r.Get("/chats/{chatId}/lease-requests", leaseHandler.ListLeaseRequests)
			r.Get("/chats/{chatId}/shared-documents", leaseHandler.ListSharedDocuments)
			r.Post("/lease-requests/{id}/accept", leaseHandler.AcceptLeaseRequest)
			r.Post("/lease-requests/{id}/decline", leaseHandler.DeclineLeaseRequest)
			r.Post("/lease-requests/{id}/cancel", leaseHandler.CancelLeaseRequest)
			r.Post("/lease-requests/{id}/rescind", leaseHandler.RescindAcceptedLeaseRequest)
			r.Patch("/lease-requests/{id}/price", leaseHandler.UpdateOfferedPrice)
			// Price-review (migration 000028): when the owner adjusts the
			// price post-acceptance, Pay Now is held until the driver
			// explicitly accepts or declines the new offer.
			r.Post("/lease-requests/{id}/accept-price", leaseHandler.AcceptPriceChange)
			r.Post("/lease-requests/{id}/decline-price", leaseHandler.DeclinePriceChange)
			r.Post("/lease-requests/{id}/pickup-confirm", leaseHandler.ConfirmPickup)
			r.Post("/lease-requests/{id}/pickup-deadline/extend", leaseHandler.ExtendPickupDeadline)

			// Payments (Stripe)
			r.Post("/lease-requests/{id}/payments/intent", leaseHandler.CreatePaymentIntent)
			r.Post("/lease-requests/{id}/payments/sync", leaseHandler.SyncPaymentStatus)

			// Key handovers (post-payment owner→driver key exchange)
			r.Get("/key-handovers/today", keyHandoverHandler.Today)
			r.Get("/key-handovers/{id}", keyHandoverHandler.Get)
			r.Post("/key-handovers/{id}/owner-confirm", keyHandoverHandler.OwnerConfirm)
			r.Post("/key-handovers/{id}/driver-confirm", keyHandoverHandler.DriverConfirm)
			r.Post("/key-handovers/{id}/dismiss", keyHandoverHandler.Dismiss)

			// Vehicle returns (end-of-rental driver→owner handshake + refund)
			r.Post("/lease-requests/{id}/vehicle-return", vehicleReturnHandler.Initiate)
			r.Get("/lease-requests/{id}/vehicle-return", vehicleReturnHandler.GetForLease)
			r.Get("/vehicle-returns/today", vehicleReturnHandler.Today)
			r.Get("/vehicle-returns/{id}", vehicleReturnHandler.Get)
			r.Post("/vehicle-returns/{id}/cancel", vehicleReturnHandler.Cancel)
			r.Post("/vehicle-returns/{id}/owner-confirm", vehicleReturnHandler.OwnerConfirm)
			r.Post("/vehicle-returns/{id}/dispute", vehicleReturnHandler.Dispute)

			// Purchase requests (buy the car flow).
			r.Post("/cars/{carId}/purchase-requests", purchaseHandler.Create)
			r.Get("/chats/{chatId}/purchase-requests", purchaseHandler.ListForChat)
			r.Get("/today/purchase-requests", purchaseHandler.Today)
			r.Get("/purchase-requests/{id}", purchaseHandler.Get)
			r.Post("/purchase-requests/{id}/cancel", purchaseHandler.Cancel)
			r.Post("/purchase-requests/{id}/accept", purchaseHandler.Accept)
			r.Post("/purchase-requests/{id}/decline", purchaseHandler.Decline)
			r.Patch("/purchase-requests/{id}/bos", purchaseHandler.UpdateBOS)
			r.Patch("/purchase-requests/{id}/bos/buyer-fields", purchaseHandler.UpdateBOSBuyerFields)
			r.Get("/purchase-requests/{id}/bos", purchaseHandler.GetBOS)
			r.Post("/purchase-requests/{id}/bos/sign", purchaseHandler.SignBOS)
			r.Post("/purchase-requests/{id}/bos/finalize", purchaseHandler.FinalizeBOS)
			r.Post("/purchase-requests/{id}/payment-intent", purchaseHandler.CreatePaymentIntent)
			r.Post("/purchase-requests/{id}/sync-payment", purchaseHandler.SyncPayment)
			r.Post("/purchase-requests/{id}/schedule-handover", purchaseHandler.ScheduleHandover)
			r.Post("/purchase-requests/{id}/keys-handed-over", purchaseHandler.KeysHandedOver)
			r.Post("/purchase-requests/{id}/inspect/accept", purchaseHandler.InspectAccept)
			r.Post("/purchase-requests/{id}/inspect/reject", purchaseHandler.InspectReject)
			r.Post("/purchase-requests/{id}/rejection-evidence", purchaseHandler.UploadEvidence)
			r.Post("/purchase-requests/{id}/rejection/withdraw", purchaseHandler.WithdrawRejection)

			// Accident reports (user-facing)
			r.Route("/accidents", func(r chi.Router) {
				r.Post("/", accidentHandler.Create)
				r.Get("/", accidentHandler.List)
				r.Get("/draft", accidentHandler.GetDraft)
				r.Route("/{id}", func(r chi.Router) {
					r.Get("/", accidentHandler.Get)
					r.Patch("/", accidentHandler.Patch)
					r.Post("/attachments", accidentHandler.Upload)
					r.Delete("/attachments/{attachId}", accidentHandler.DeleteAttachment)
					r.Post("/sign", accidentHandler.Sign)
					r.Post("/submit", accidentHandler.Submit)
				})
			})

			// Support chat (user-facing)
			r.Route("/support", func(r chi.Router) {
				r.Post("/chats", supportHandler.GetOrCreate)
				r.Route("/chats/{chatId}", func(r chi.Router) {
					r.Get("/messages", supportHandler.ListMessages)
					r.Post("/messages", supportHandler.SendMessage)
					r.Post("/read", supportHandler.MarkRead)
				})
			})

			// Admin panel API — require role=admin
			r.Route("/admin", func(r chi.Router) {
				r.Use(middleware.RequireRole(models.RoleAdmin))

				r.Get("/users", adminHandler.ListUsers)
				r.Get("/users/{id}", adminHandler.GetUser)
				r.Patch("/users/{id}/block", adminHandler.BlockUser)
				r.Patch("/users/{id}/profile", adminHandler.UpdateUserProfile)
				// Admin-triggered password reset (D7): 202, never returns
				// the token — the user gets the standard reset email.
				r.Post("/users/{id}/reset-password", adminHandler.ResetUserPassword)

				r.Get("/cars", adminHandler.ListCars)
				r.Get("/cars/{id}", adminHandler.GetCar)
				r.Patch("/cars/{id}/approve", adminHandler.ApproveCar)

				r.Get("/chats", adminHandler.ListChats)
				r.Get("/chats/{id}/messages", adminHandler.ListChatMessages)
				r.Post("/chats/{id}/messages", adminHandler.SendChatMessage)

				r.Get("/rents", adminHandler.ListRents)
				r.Get("/rents/{id}", adminHandler.GetRent)

				r.Get("/support/chats", adminHandler.ListSupportChats)
				r.Get("/support/chats/{id}/messages", adminHandler.ListSupportMessages)
				r.Post("/support/chats/{id}/messages", adminHandler.SendSupportMessage)
				r.Post("/support/chats/{id}/read", adminHandler.MarkSupportChatRead)

				r.Get("/accidents", adminHandler.ListAccidents)
				r.Get("/accidents/{id}", adminHandler.GetAccident)
				r.Patch("/accidents/{id}/status", adminHandler.UpdateAccidentStatus)

				r.Get("/car-sells", adminHandler.ListCarSells)
				r.Get("/car-sells/{id}", adminHandler.GetCarSell)

				// Vehicle returns — admin list + dispute resolution.
				r.Get("/vehicle-returns", vehicleReturnHandler.AdminList)
				r.Post("/vehicle-returns/{id}/resolve", vehicleReturnHandler.AdminResolve)

				// Purchase requests + rejections.
				r.Get("/purchase-requests", purchaseHandler.AdminList)
				r.Get("/purchase-requests/{id}", purchaseHandler.AdminGet)
				r.Post("/purchase-requests/{id}/retry-refund", purchaseHandler.AdminRetryRefund)
				r.Get("/purchase-rejections", purchaseHandler.AdminListRejections)
				r.Post("/purchase-rejections/{id}/resolve", purchaseHandler.AdminResolveRejection)
			})
		})
	})

	// Serve uploaded files. Private paths (chat attachments, driver
	// documents, accident files, …) require a valid `?sig=…&exp=…` HMAC
	// minted by the API handler that knew the caller was authorized.
	// Public paths (car photos, profile photos) are served unsigned.
	filesHandler := handlers.NewFilesHandler(uploadDir, uploadSigner, cfg.RequirePrivateUploadSignatures, logger)
	r.Get("/uploads/*", filesHandler.Serve)

	// Serve OpenAPI spec
	r.Get("/openapi", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		spec, err := staticFiles.ReadFile("static/openapi.yaml")
		if err != nil {
			http.Error(w, "OpenAPI spec not found", http.StatusNotFound)
			return
		}
		w.Write(spec)
	})

	// Serve Swagger UI
	r.Get("/docs", func(w http.ResponseWriter, r *http.Request) {
		html := `<!DOCTYPE html>
<html>
<head>
    <title>DriveBai API - Swagger UI</title>
    <link rel="stylesheet" type="text/css" href="https://unpkg.com/swagger-ui-dist@5.9.0/swagger-ui.css">
</head>
<body>
    <div id="swagger-ui"></div>
    <script src="https://unpkg.com/swagger-ui-dist@5.9.0/swagger-ui-bundle.js"></script>
    <script>
        window.onload = function() {
            SwaggerUIBundle({
                url: "/openapi",
                dom_id: '#swagger-ui',
                presets: [
                    SwaggerUIBundle.presets.apis,
                    SwaggerUIBundle.SwaggerUIStandalonePreset
                ],
                layout: "BaseLayout"
            });
        }
    </script>
</body>
</html>`
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(html))
	})

	// Root redirect to docs
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/docs", http.StatusMovedPermanently)
	})

	// Start server
	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Graceful shutdown
	go func() {
		logger.Info("starting server", "port", cfg.Port, "env", cfg.Env)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Background worker: pickup-deadline expiry scanner. Cancelled on shutdown
	// via workerCtx so in-flight Stripe refunds get to finish (or fail) before
	// the process exits — and resume on the next boot since state is in pgsql.
	workerCtx, workerCancel := context.WithCancel(context.Background())
	defer workerCancel()
	scanInterval := time.Duration(cfg.PickupExpiryScanIntervalSeconds) * time.Second
	go leaseHandler.StartPickupExpiryScanner(workerCtx, scanInterval)
	// Same cadence is fine for vehicle-return refund retries — they share
	// the same Stripe budget and the same "stuck Refund" semantics. If the
	// refund call succeeded but FinalizeRefund failed, the stable idempotency
	// key makes the replay a server-side no-op at Stripe.
	go vehicleReturnHandler.StartReturnRefundScanner(workerCtx, scanInterval)
	// Purchase expiry: sweeps both offer-TTL and Stripe manual-capture
	// auth-TTL rows. Same cadence keeps ops budgets aligned.
	go purchaseHandler.StartExpiryScanner(workerCtx, scanInterval)

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("server forced to shutdown", "error", err)
	}

	logger.Info("server stopped")
}

func init() {
	// Ensure static directory exists for embed
	_ = staticFiles
	fmt.Println("DriveBai API Server")
}
