package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/drivebai/backend/internal/httputil"
	"github.com/drivebai/backend/internal/models"
	"github.com/drivebai/backend/internal/repository"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/drivebai/backend/internal/ws"
)

// PayoutHandler owns the owner-money surface: Connect onboarding sessions,
// the account-status mirror, the Connect webhook, the payout ledger engine
// (create → execute → retry), the escrow aging sweep, and the admin views.
//
// Money model (Part 1, confirmed): separate charges and transfers. The
// driver's charge lands whole in the platform balance; the owner's share
// moves as a Transfer when the rental settles. Because every refund in the
// lease lifecycle happens BEFORE settlement, clawbacks are structurally
// impossible on this path.
type PayoutHandler struct {
	payoutRepo   *repository.PayoutRepository
	leaseRepo    *repository.LeaseRequestRepository
	userRepo     *repository.UserRepository
	ticketRepo   *repository.TicketRepository
	stripe       *stripeService.Service
	wsHub        *ws.Hub
	notifHandler *NotificationHandler
	feeBPS       int
	logger       *slog.Logger
	// purchaseRepo lets a SALE payout find its funding charge. Optional:
	// nil simply means the transfer comes from the platform balance rather
	// than being traced to the specific charge.
	purchaseRepo *repository.PurchaseRequestRepository
}

// SetPurchaseRepository wires the purchase side so a completed car sale can
// pay its seller. Setter, per the house pattern.
func (h *PayoutHandler) SetPurchaseRepository(p *repository.PurchaseRequestRepository) {
	h.purchaseRepo = p
}

func NewPayoutHandler(
	payoutRepo *repository.PayoutRepository,
	leaseRepo *repository.LeaseRequestRepository,
	userRepo *repository.UserRepository,
	ticketRepo *repository.TicketRepository,
	stripe *stripeService.Service,
	wsHub *ws.Hub,
	notifHandler *NotificationHandler,
	feeBPS int,
	logger *slog.Logger,
) *PayoutHandler {
	return &PayoutHandler{
		payoutRepo:   payoutRepo,
		leaseRepo:    leaseRepo,
		userRepo:     userRepo,
		ticketRepo:   ticketRepo,
		stripe:       stripe,
		wsHub:        wsHub,
		notifHandler: notifHandler,
		feeBPS:       feeBPS,
		logger:       logger,
	}
}

// ─── Owner endpoints ────────────────────────────────────────────────────────

// CreateOnboardingSession — POST /api/v1/payout-account/session.
// Creates the connected account on first call (idempotent per user), then
// mints an account session for the embedded onboarding component. The iOS
// launch sits behind a feature flag until the SDK ships (amendment ③), but
// the endpoint is complete and test-verified now.
func (h *PayoutHandler) CreateOnboardingSession(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	user, err := h.userRepo.GetByID(r.Context(), userID)
	if err != nil || user == nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrUserNotFound)
		return
	}

	accountID, _, err := h.payoutRepo.GetPayoutAccount(r.Context(), userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrUserNotFound)
		return
	}
	if accountID == nil {
		acct, cerr := h.stripe.CreateConnectedAccount(user.Email, userID.String())
		if cerr != nil {
			h.logger.Error("payout: create connected account", "error", cerr, "user_id", userID)
			httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "Couldn't start payout setup — try again in a moment"))
			return
		}
		if serr := h.payoutRepo.SetStripeAccount(r.Context(), userID, acct.ID); serr != nil {
			h.logger.Error("payout: store account id", "error", serr, "user_id", userID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		accountID = &acct.ID
	}

	sess, err := h.stripe.CreateAccountSession(*accountID)
	if err != nil && strings.Contains(err.Error(), "resource_missing") {
		// The stored account no longer exists at Stripe (deleted or
		// rejected-and-removed). Without recovery this dead-ends the owner
		// forever (E2E finding) — forget it and start a fresh account.
		h.logger.Warn("payout: stored account gone at Stripe — recreating", "stale_account", *accountID, "user_id", userID)
		if cerr := h.payoutRepo.ClearStripeAccount(r.Context(), userID, *accountID); cerr != nil {
			h.logger.Error("payout: clear stale account", "error", cerr, "user_id", userID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		acct, cerr := h.stripe.CreateConnectedAccount(user.Email, userID.String())
		if cerr != nil {
			h.logger.Error("payout: recreate connected account", "error", cerr, "user_id", userID)
			httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "Couldn't start payout setup — try again in a moment"))
			return
		}
		if serr := h.payoutRepo.SetStripeAccount(r.Context(), userID, acct.ID); serr != nil {
			h.logger.Error("payout: store recreated account id", "error", serr, "user_id", userID)
			httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
			return
		}
		accountID = &acct.ID
		sess, err = h.stripe.CreateAccountSession(*accountID)
	}
	if err != nil {
		h.logger.Error("payout: create account session", "error", err, "user_id", userID)
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "Couldn't start payout setup — try again in a moment"))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]string{
		"client_secret":   sess.ClientSecret,
		"publishable_key": h.stripe.PublishableKey(),
		"account_id":      *accountID,
	})
}

// GetPayoutAccount — GET /api/v1/payout-account. The app's single source
// for what to render: precise status, what Stripe still needs, and the
// earnings picture.
func (h *PayoutHandler) GetPayoutAccount(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	accountID, localStatus, err := h.payoutRepo.GetPayoutAccount(r.Context(), userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrUserNotFound)
		return
	}

	resp := map[string]interface{}{"status": localStatus}
	if accountID != nil {
		// Live refresh: cheap, keeps the mirror honest even if a webhook
		// was missed, and updates the local row as a side effect.
		if acct, aerr := h.stripe.GetConnectedAccount(*accountID); aerr == nil {
			status := mapAccountStatus(acct)
			reqJSON, _ := json.Marshal(acct.Requirements)
			if _, _, uerr := h.payoutRepo.UpdatePayoutStatus(r.Context(), *accountID, status, reqJSON); uerr != nil {
				h.logger.Warn("payout: mirror refresh", "error", uerr)
			}
			resp["status"] = status
			resp["payouts_enabled"] = acct.PayoutsEnabled
			resp["currently_due"] = acct.Requirements.CurrentlyDue
			resp["past_due"] = acct.Requirements.PastDue
			resp["disabled_reason"] = acct.Requirements.DisabledReason
			// Part A: the app shows the connected bank and when money
			// actually lands, natively.
			if len(acct.ExternalAccounts.Data) > 0 {
				resp["bank_name"] = acct.ExternalAccounts.Data[0].BankName
				resp["bank_last4"] = acct.ExternalAccounts.Data[0].Last4
			}
			if acct.Settings.Payouts.Schedule.Interval != "" {
				resp["payout_schedule"] = map[string]interface{}{
					"interval":   acct.Settings.Payouts.Schedule.Interval,
					"delay_days": acct.Settings.Payouts.Schedule.DelayDays,
				}
			}
			if acct.Requirements.CurrentDeadline != nil {
				resp["current_deadline"] = time.Unix(*acct.Requirements.CurrentDeadline, 0).UTC().Format(time.RFC3339)
			}
		} else {
			h.logger.Warn("payout: live account read failed, serving local mirror", "error", aerr)
		}
	}

	// Earnings summary from the ledger.
	payouts, perr := h.payoutRepo.ListForOwner(r.Context(), userID)
	if perr == nil {
		var paid, awaiting, pending int64
		for _, p := range payouts {
			switch p.Status {
			case models.PayoutPaid:
				paid += p.OwnerAmountCents
			case models.PayoutAwaitingOnboarding:
				awaiting += p.OwnerAmountCents
			case models.PayoutPending, models.PayoutFailed:
				pending += p.OwnerAmountCents
			}
		}
		resp["earnings"] = map[string]int64{
			"paid_cents":     paid,
			"awaiting_cents": awaiting,
			"pending_cents":  pending,
		}
	}
	httputil.WriteJSON(w, http.StatusOK, resp)
}

// CreateDashboardLink — POST /api/v1/payout-account/dashboard-link.
// Mints a one-time Stripe Express dashboard URL — the supported surface
// for viewing/replacing the bank account under our controller config (the
// native account-management component is Stripe-internal SPI on iOS).
// Stripe SMS-authenticates on entry; the app's copy sets that expectation.
// Without an account there is nothing to manage: 409 routes the client
// back to onboarding, never a dead end.
func (h *PayoutHandler) CreateDashboardLink(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	accountID, status, err := h.payoutRepo.GetPayoutAccount(r.Context(), userID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, models.ErrUserNotFound)
		return
	}
	// A non-onboarded owner (no account, or details never submitted) has
	// no bank to manage — 409 routes the client into onboarding instead.
	// ready/action_needed/restricted all legitimately need the dashboard
	// (that's where a bank problem gets fixed).
	if accountID == nil || status == models.PayoutAccountNone || status == models.PayoutAccountOnboarding {
		httputil.WriteError(w, http.StatusConflict, models.NewAPIError("PAYOUT_NOT_SET_UP", "Finish payout setup first — there's no account to manage yet"))
		return
	}
	link, err := h.stripe.CreateLoginLink(*accountID)
	if err != nil {
		h.logger.Error("payout: create dashboard link", "error", err, "user_id", userID)
		httputil.WriteError(w, http.StatusBadGateway, models.NewAPIError("STRIPE_ERROR", "Couldn't open payout settings — try again in a moment"))
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]string{"url": link})
}

// ListMyPayouts — GET /api/v1/payout-account/payouts.
func (h *PayoutHandler) ListMyPayouts(w http.ResponseWriter, r *http.Request) {
	userID, ok := httputil.GetUserID(r.Context())
	if !ok {
		httputil.WriteError(w, http.StatusUnauthorized, models.ErrUnauthorized)
		return
	}
	rows, err := h.payoutRepo.ListForOwner(r.Context(), userID)
	if err != nil {
		h.logger.Error("payout: list for owner", "error", err, "user_id", userID)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"payouts": rows})
}

// ─── Status mapping (pure — unit-tested) ────────────────────────────────────

// mapAccountStatus folds the account object into the coarse status the app
// renders. Order matters: never-submitted beats everything (a FRESH account
// already carries disabled_reason=requirements.past_due, and "one more
// thing is needed" is the wrong first impression — E2E finding), then
// restriction, then the action-needed states, then readiness.
func mapAccountStatus(acct *stripeService.ConnectedAccount) models.UserPayoutStatus {
	if !acct.DetailsSubmitted && !acct.PayoutsEnabled {
		return models.PayoutAccountOnboarding
	}
	r := acct.Requirements
	if r.DisabledReason != nil && *r.DisabledReason != "" {
		switch {
		case *r.DisabledReason == "requirements.pending_verification":
			return models.PayoutAccountPendingVerif
		case *r.DisabledReason == "requirements.past_due" || *r.DisabledReason == "action_required.requested_capabilities":
			return models.PayoutAccountActionNeeded
		default:
			// under_review, rejected.*, listed, platform_paused…
			return models.PayoutAccountRestricted
		}
	}
	if acct.PayoutsEnabled {
		if len(r.CurrentlyDue) > 0 || len(r.PastDue) > 0 {
			return models.PayoutAccountActionNeeded
		}
		return models.PayoutAccountReady
	}
	if !acct.DetailsSubmitted {
		return models.PayoutAccountOnboarding
	}
	if len(r.PendingVerification) > 0 {
		return models.PayoutAccountPendingVerif
	}
	if len(r.CurrentlyDue) > 0 || len(r.PastDue) > 0 {
		return models.PayoutAccountActionNeeded
	}
	return models.PayoutAccountPendingVerif
}

// ─── Connect webhook ────────────────────────────────────────────────────────

// HandleConnectWebhook — POST /api/v1/stripe/connect-webhook. SEPARATE
// endpoint from the payment webhook: Connect events come from connected
// accounts (top-level "account" field) and are signed with their own
// secret. The endpoint at Stripe is created with connect=true AND
// api_version pinned to 2025-02-24.acacia — the same version trap that
// broke the payment webhook once already.
func (h *PayoutHandler) HandleConnectWebhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, 65536))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("read error"))
		return
	}
	sig := r.Header.Get("Stripe-Signature")
	if sig == "" {
		h.logger.Warn("connect webhook: missing Stripe-Signature header")
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("missing signature"))
		return
	}
	if err := h.stripe.VerifyConnectWebhookSignature(payload, sig); err != nil {
		h.logger.Warn("connect webhook: signature verification failed", "error", err)
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid signature"))
		return
	}

	var event struct {
		Type    string `json:"type"`
		Account string `json:"account"`
		Data    struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, models.NewValidationError("invalid payload"))
		return
	}
	h.logger.Info("connect webhook: event received", "type", event.Type, "account", event.Account, "verified", true)

	switch event.Type {
	case "account.updated", "capability.updated", "account.external_account.updated":
		// The account object rides in account.updated; for the other two,
		// re-read the account. Either way, fold into the local mirror.
		var acct *stripeService.ConnectedAccount
		if event.Type == "account.updated" {
			var a stripeService.ConnectedAccount
			if err := json.Unmarshal(event.Data.Object, &a); err == nil && a.ID != "" {
				acct = &a
			}
		}
		if acct == nil && event.Account != "" {
			if a, aerr := h.stripe.GetConnectedAccount(event.Account); aerr == nil {
				acct = a
			}
		}
		if acct != nil {
			h.applyAccountUpdate(r.Context(), acct)
		}
	case "payout.failed":
		// A payout from the owner's Stripe balance to their BANK failed —
		// their money is safe in their Stripe balance, but they should fix
		// the bank details. Notify; the ledger row stays 'paid' (the
		// platform's transfer did land).
		if event.Account != "" {
			if userID, _, err := h.payoutRepo.UpdatePayoutStatus(r.Context(), event.Account, models.PayoutAccountActionNeeded, nil); err == nil {
				go h.notifHandler.Notify(userID, models.NotificationTypePayment,
					"Bank payout failed",
					"A payout to your bank didn't go through. Check your bank details in payout settings — your money is safe and will retry.",
					nil, nil)
			}
		}
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]bool{"received": true})
}

// applyAccountUpdate folds a fresh account state into the mirror, notifies
// the owner on meaningful transitions, and — the payoff — executes any
// payouts that were waiting on onboarding the moment the account is ready.
func (h *PayoutHandler) applyAccountUpdate(ctx context.Context, acct *stripeService.ConnectedAccount) {
	status := mapAccountStatus(acct)
	reqJSON, _ := json.Marshal(acct.Requirements)
	userID, previous, err := h.payoutRepo.UpdatePayoutStatus(ctx, acct.ID, status, reqJSON)
	if err != nil {
		h.logger.Warn("connect webhook: no local user for account", "account", acct.ID, "error", err)
		return
	}
	if previous == models.UserPayoutStatus("") || previous == status {
		if status == models.PayoutAccountReady {
			h.executeAwaitingForOwner(ctx, userID)
		}
		return
	}

	h.logger.Info("payout status transition", "user_id", userID, "from", previous, "to", status)
	switch status {
	case models.PayoutAccountReady:
		go h.notifHandler.Notify(userID, models.NotificationTypePayment,
			"Payouts are set up",
			"You're all set to receive rental payouts. Anything already earned is on its way.",
			nil, nil)
		h.executeAwaitingForOwner(ctx, userID)
	case models.PayoutAccountActionNeeded:
		go h.notifHandler.Notify(userID, models.NotificationTypePayment,
			"Payout setup needs attention",
			"Stripe needs one more thing to keep your payouts running. Open Earnings & payouts to see exactly what.",
			nil, nil)
	case models.PayoutAccountRestricted:
		go h.notifHandler.Notify(userID, models.NotificationTypePayment,
			"Payouts paused",
			"Your payout account was paused by Stripe. Open Earnings & payouts for details — rentals you complete are held safely until this is resolved.",
			nil, nil)
	}
}

// ─── The ledger engine ──────────────────────────────────────────────────────

// SettleRentalPayout creates (idempotently) the ledger row for a settled
// rental and executes it if the owner is ready. keptCents is what the
// driver's payment left after refunds; source records why. Called from the
// vehicle-return completion path and from admin settlement.
func (h *PayoutHandler) SettleRentalPayout(ctx context.Context, leaseID, ownerID uuid.UUID, keptCents int64, source models.OwnerPayoutSource, note *string) {
	if keptCents <= 0 {
		h.logger.Info("payout: nothing kept, no payout due", "lease_request_id", leaseID)
		return
	}
	fee, ownerShare := models.ComputePayoutSplit(keptCents, h.feeBPS)

	accountID, status, aerr := h.payoutRepo.GetPayoutAccount(ctx, ownerID)
	rowStatus := models.PayoutPending
	if aerr != nil || accountID == nil || status != models.PayoutAccountReady {
		rowStatus = models.PayoutAwaitingOnboarding
	}

	row, created, err := h.payoutRepo.Create(ctx, &models.OwnerPayout{
		LeaseRequestID:   &leaseID,
		OwnerID:          ownerID,
		StripeAccountID:  accountID,
		GrossKeptCents:   keptCents,
		FeeBPS:           h.feeBPS,
		FeeCents:         fee,
		OwnerAmountCents: ownerShare,
		Currency:         "USD",
		Status:           rowStatus,
		Source:           source,
		Note:             note,
	})
	if err != nil {
		h.logger.Error("payout: create ledger row", "error", err, "lease_request_id", leaseID)
		return
	}
	if !created {
		if row.GrossKeptCents != keptCents {
			// M4: the silent ON CONFLICT DO NOTHING used to swallow exactly
			// the double-spend case — a second settlement arriving with a
			// DIFFERENT kept amount (payout_only earlier, refunding close
			// later). The handlers now guard against reaching this, so a
			// mismatch here is a real invariant break: surface it loudly.
			h.logger.Error("payout: LEDGER MISMATCH — settlement amount disagrees with existing row",
				"lease_request_id", leaseID,
				"existing_gross_kept_cents", row.GrossKeptCents,
				"attempted_kept_cents", keptCents,
				"existing_status", row.Status)
			if h.ticketRepo != nil {
				desc := fmt.Sprintf(
					"Payout ledger row for lease %s holds gross_kept %d¢ but a settlement just recomputed kept as %d¢ (existing status %q). One charge may be funding both an owner payout and a driver refund — reconcile against the Stripe dashboard before any further payout action.",
					leaseID, row.GrossKeptCents, keptCents, row.Status)
				if _, terr := h.ticketRepo.CreateSystemTicket(ctx, ownerID, models.TicketCategoryPayments,
					"Payout ledger mismatch needs reconciliation", desc, &leaseID, nil); terr != nil {
					h.logger.Error("payout: mismatch ticket", "error", terr, "lease_request_id", leaseID)
				}
			}
			return
		}
		h.logger.Info("payout: ledger row already exists (idempotent)", "lease_request_id", leaseID, "status", row.Status)
		return
	}

	if rowStatus == models.PayoutAwaitingOnboarding {
		go h.notifHandler.Notify(ownerID, models.NotificationTypePayment,
			fmt.Sprintf("%s is waiting for you", formatMoney(ownerShare)),
			fmt.Sprintf("Your rental earned %s. Finish payout setup in Earnings & payouts and it transfers automatically.", formatMoney(ownerShare)),
			nil, &leaseID)
		return
	}
	h.executePayout(ctx, row)
}

// executePayout fires the Transfer. Stable idempotency key per lease —
// retries can never double-pay.
func (h *PayoutHandler) executePayout(ctx context.Context, row *models.OwnerPayout) {
	accountID, status, err := h.payoutRepo.GetPayoutAccount(ctx, row.OwnerID)
	if err != nil || accountID == nil || status != models.PayoutAccountReady {
		if merr := h.payoutRepo.MarkAwaitingOnboarding(ctx, row.ID); merr != nil {
			h.logger.Error("payout: park awaiting onboarding", "error", merr, "payout_id", row.ID)
		}
		return
	}

	// Reconciliation first: if a transfer for this lease already exists
	// (crash between transfer and MarkPaid, or any ambiguity), adopt it —
	// never create a second one. This, not the idempotency key, is the
	// double-pay guard: Stripe caches ERROR responses under an idempotency
	// key for 24h, so a stable key would replay one transient failure
	// forever (observed in E2E).
	// source_transaction + dispute-addressability: resolve the funding
	// charge up front — MarkPaid stamps it as source_charge_id so a lost
	// chargeback can target exactly this row (review CRITICAL: the column
	// was never written before, making the clawback dead code).
	sourceCharge := ""
	switch {
	case row.PurchaseRequestID != nil:
		// A sale funds its transfer from the captured purchase charge.
		if h.purchaseRepo != nil {
			if pr, perr := h.purchaseRepo.GetByID(ctx, *row.PurchaseRequestID); perr == nil && pr != nil && pr.PaymentIntentID != nil {
				if chargeID, cerr := h.stripe.GetLatestChargeID(*pr.PaymentIntentID); cerr == nil {
					sourceCharge = chargeID
				} else {
					h.logger.Warn("payout: sale charge lookup failed, transferring from balance",
						"error", cerr, "purchase_request_id", *row.PurchaseRequestID)
				}
			}
		}
	case row.BillingCycleID != nil:
		// Rolling cycle rows carry their funding charge from accrual time.
		if row.SourceChargeID != nil {
			sourceCharge = *row.SourceChargeID
		}
	case row.LeaseRequestID != nil:
		if payment, perr := h.leaseRepo.GetPaymentByLeaseRequestID(ctx, *row.LeaseRequestID); perr == nil && payment != nil && payment.PaymentIntentID != nil {
			if chargeID, cerr := h.stripe.GetLatestChargeID(*payment.PaymentIntentID); cerr == nil {
				sourceCharge = chargeID
			} else {
				h.logger.Warn("payout: charge lookup failed, transferring from balance", "error", cerr, "lease_request_id", row.LeaseRequestID)
			}
		}
	}

	// The transfer group is the adoption key: it must identify THIS payout
	// uniquely, or a crash between transfer and MarkPaid could adopt somebody
	// else's transfer.
	var transferGroup string
	switch {
	case row.PurchaseRequestID != nil:
		transferGroup = "sale-" + row.PurchaseRequestID.String()
	case row.BillingCycleID != nil && row.LeaseRequestID != nil:
		// Per-cycle group: FindTransferByGroup adoption stays exact for
		// every cycle independently; legacy rows keep the lease-wide group.
		transferGroup = "lease-" + row.LeaseRequestID.String() + "-cycle-" + row.BillingCycleID.String()
	case row.LeaseRequestID != nil:
		transferGroup = "lease-" + row.LeaseRequestID.String()
	default:
		h.logger.Error("payout: row has no source, refusing to transfer", "payout_id", row.ID)
		return
	}
	if existing, ferr := h.stripe.FindTransferByGroup(transferGroup); ferr == nil && existing != nil {
		h.logger.Info("payout: adopting existing transfer", "payout_id", row.ID, "transfer_id", existing.ID)
		if paid, merr := h.payoutRepo.MarkPaid(ctx, row.ID, existing.ID, *accountID, sourceCharge); merr == nil {
			leaseRef := paid.LeaseRequestID
			go h.notifHandler.Notify(paid.OwnerID, models.NotificationTypePayment,
				fmt.Sprintf("%s sent to your bank", formatMoney(paid.OwnerAmountCents)),
				fmt.Sprintf("Your %s payout of %s is on its way (platform fee %s).", payoutKindWord(paid), formatMoney(paid.OwnerAmountCents), formatMoney(paid.FeeCents)),
				nil, leaseRef)
		}
		return
	}

	// Per-attempt idempotency: updated_at changes on every MarkFailed, so
	// each retry gets a fresh key while any single attempt stays
	// double-send-safe. The reconciliation above catches the crashed
	//-after-success case a varying key alone could not.
	idemKey := fmt.Sprintf("payout-%s-%d", row.ID, row.UpdatedAt.Unix())
	tr, err := h.stripe.CreateTransfer(*accountID, row.OwnerAmountCents, row.Currency,
		sourceCharge, transferGroup, idemKey)
	if err != nil {
		h.logger.Error("payout: transfer failed", "error", err, "payout_id", row.ID)
		if merr := h.payoutRepo.MarkFailed(ctx, row.ID, err.Error()); merr != nil {
			h.logger.Error("payout: mark failed", "error", merr, "payout_id", row.ID)
		}
		return
	}
	paid, err := h.payoutRepo.MarkPaid(ctx, row.ID, tr.ID, *accountID, sourceCharge)
	if err != nil {
		// The transfer DID land; the next sweep's FindTransferByGroup
		// reconciliation adopts it and MarkPaid then succeeds.
		h.logger.Error("payout: transfer landed but MarkPaid failed — sweep will reconcile", "error", err, "payout_id", row.ID, "transfer_id", tr.ID)
		return
	}
	leaseRef := paid.LeaseRequestID
	go h.notifHandler.Notify(paid.OwnerID, models.NotificationTypePayment,
		fmt.Sprintf("%s sent to your bank", formatMoney(paid.OwnerAmountCents)),
		fmt.Sprintf("Your rental payout of %s is on its way (platform fee %s).", formatMoney(paid.OwnerAmountCents), formatMoney(paid.FeeCents)),
		nil, leaseRef)
	h.logger.Info("payout: paid", "payout_id", paid.ID, "transfer_id", tr.ID, "amount_cents", paid.OwnerAmountCents)
}

// executeAwaitingForOwner drains the escrow the moment an owner becomes
// ready.
func (h *PayoutHandler) executeAwaitingForOwner(ctx context.Context, ownerID uuid.UUID) {
	rows, err := h.payoutRepo.ListForOwner(ctx, ownerID)
	if err != nil {
		h.logger.Error("payout: list for ready owner", "error", err)
		return
	}
	for i := range rows {
		if rows[i].Status == models.PayoutAwaitingOnboarding || rows[i].Status == models.PayoutFailed {
			h.executePayout(ctx, &rows[i])
		}
	}
}

// ─── Sweep scanner ──────────────────────────────────────────────────────────

// payoutFailedRetryAfter matches the return-refund scanner's 2-minute
// staleness. It matters on the very first payout: Stripe can report
// payouts_enabled seconds before the transfers capability finishes
// activating (observed in E2E — insufficient_capabilities_for_transfer),
// and the sweep retry is the designed recovery for that race.
const payoutFailedRetryAfter = 2 * time.Minute

// StartPayoutSweep retries failed transfers, executes rows whose owner
// became ready, sends escrow reminders on the weekly cadence, and opens ONE
// escalation ticket per stale balance (ticket-first, flag-after — the
// crash-safe shape). House scanner pattern throughout.
func (h *PayoutHandler) StartPayoutSweep(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	h.logger.Info("payout sweep started", "interval", interval.String(), "fee_bps", h.feeBPS)
	for {
		select {
		case <-ctx.Done():
			h.logger.Info("payout sweep stopped")
			return
		case <-ticker.C:
			h.runPayoutSweep(ctx)
		}
	}
}

func (h *PayoutHandler) runPayoutSweep(ctx context.Context) {
	now := time.Now().UTC()

	executable, err := h.payoutRepo.ListExecutable(ctx, now.Add(-payoutFailedRetryAfter), 50)
	if err != nil {
		h.logger.Error("payout sweep: list executable", "error", err)
	}
	for i := range executable {
		h.executePayout(ctx, &executable[i])
	}

	// Escrow reminders — weekly, claimed-once per window.
	reminders, err := h.payoutRepo.ClaimEscrowReminders(ctx,
		now.Add(-models.PayoutEscrowReminderEvery), now.Add(-models.PayoutEscrowReminderEvery), 50)
	if err != nil {
		h.logger.Error("payout sweep: claim reminders", "error", err)
	}
	for _, p := range reminders {
		leaseRef := p.LeaseRequestID
		go h.notifHandler.Notify(p.OwnerID, models.NotificationTypePayment,
			fmt.Sprintf("%s still waiting for you", formatMoney(p.OwnerAmountCents)),
			fmt.Sprintf("Your rental earnings of %s have been waiting %d days. Finish payout setup in Earnings & payouts — it takes about five minutes.",
				formatMoney(p.OwnerAmountCents), int(now.Sub(p.CreatedAt).Hours()/24)),
			nil, leaseRef)
	}

	// Escalation: stale unclaimed balances become a human's problem.
	if h.ticketRepo == nil {
		return
	}
	stale, err := h.payoutRepo.ListEscrowEscalationCandidates(ctx, now.Add(-models.PayoutEscrowEscalateAfter), 50)
	if err != nil {
		h.logger.Error("payout sweep: list escalation candidates", "error", err)
		return
	}
	for _, p := range stale {
		leaseRef := p.LeaseRequestID
		subject := fmt.Sprintf("Unclaimed owner balance — %s", formatMoney(p.OwnerAmountCents))
		description := fmt.Sprintf(
			"An owner payout has sat unclaimed for %d+ days because payout onboarding was never completed.\n\nOwner: %s\nAmount: %s (fee already carved out: %s)\nLease request: %s\nReminders sent: %d\n\nPolicy beyond this point is a business decision — see the escrow policy. Do not convert or forfeit without an explicit decision.",
			int(models.PayoutEscrowEscalateAfter.Hours()/24), p.OwnerID, formatMoney(p.OwnerAmountCents), formatMoney(p.FeeCents), p.LeaseRequestID, p.ReminderCount)
		created, terr := h.ticketRepo.CreateSystemTicket(ctx, p.OwnerID, models.TicketCategoryPayments, subject, description, leaseRef, nil)
		if terr != nil {
			h.logger.Error("payout sweep: escalation ticket failed", "error", terr, "payout_id", p.ID)
			continue
		}
		_ = created
		if merr := h.payoutRepo.MarkEscrowEscalated(ctx, p.ID); merr != nil {
			h.logger.Error("payout sweep: mark escalated", "error", merr, "payout_id", p.ID)
		}
	}
}

// LedgerRow exposes the per-lease ledger row for surfaces that don't hold
// the repo (admin settle response, admin rent drawer).
func (h *PayoutHandler) LedgerRow(ctx context.Context, leaseID uuid.UUID) (*models.OwnerPayout, error) {
	return h.payoutRepo.GetByLeaseRequestID(ctx, leaseID)
}

// AdminWithhold is the "explicitly and deliberately not paid" leg of the
// settlement matrix: it records the decision AND its reason in the ledger.
// Works whether or not a row already exists (an awaiting_onboarding balance
// can be withheld); refuses to withhold money that already moved.
func (h *PayoutHandler) AdminWithhold(ctx context.Context, leaseID, ownerID uuid.UUID, keptCents int64, note string) (*models.OwnerPayout, error) {
	fee, ownerShare := models.ComputePayoutSplit(keptCents, h.feeBPS)
	accountID, _, _ := h.payoutRepo.GetPayoutAccount(ctx, ownerID)
	row, created, err := h.payoutRepo.Create(ctx, &models.OwnerPayout{
		LeaseRequestID:   &leaseID,
		OwnerID:          ownerID,
		StripeAccountID:  accountID,
		GrossKeptCents:   keptCents,
		FeeBPS:           h.feeBPS,
		FeeCents:         fee,
		OwnerAmountCents: ownerShare,
		Currency:         "USD",
		Status:           models.PayoutWithheld,
		Source:           models.PayoutSourceAdminSettlement,
		Note:             &note,
	})
	if err != nil {
		return nil, err
	}
	if !created {
		row, err = h.payoutRepo.Withhold(ctx, row.ID, note)
		if err != nil {
			return nil, err
		}
	}
	go h.notifHandler.Notify(ownerID, models.NotificationTypePayment,
		"A rental payout was withheld",
		fmt.Sprintf("Support withheld the payout for one of your rentals. Reason: %s. Contact support if you believe this is a mistake.", note),
		nil, row.LeaseRequestID)
	return row, nil
}

// AdminReviveWithheld reverses a withhold: the row re-opens as pending and
// the engine pays it (or parks it awaiting onboarding). The reversal note
// replaces the withhold note.
func (h *PayoutHandler) AdminReviveWithheld(ctx context.Context, id uuid.UUID, note string) (*models.OwnerPayout, error) {
	row, err := h.payoutRepo.ReviveWithheld(ctx, id, note)
	if err != nil {
		return nil, err
	}
	h.executePayout(ctx, row)
	return h.payoutRepo.GetByLeaseRequestID(ctx, *row.LeaseRequestID)
}

// ─── Admin ──────────────────────────────────────────────────────────────────

// AdminListPayouts — GET /api/v1/admin/payouts?status= — every owner
// balance with its age, oldest first.
func (h *PayoutHandler) AdminListPayouts(w http.ResponseWriter, r *http.Request) {
	rows, err := h.payoutRepo.AdminList(r.Context(), r.URL.Query().Get("status"), 100)
	if err != nil {
		h.logger.Error("admin list payouts", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, models.ErrInternalError)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, map[string]interface{}{"payouts": rows})
}

// SettleSalePayout pays the SELLER of a completed car sale.
//
// Settlement point, argued the same way transfer-at-completion was argued for
// rentals: money can move backwards on a sale too, so the transfer waits
// until it practically cannot. Not at authorization (no money exists). Not at
// capture — capture happens at or before handover while the buyer still holds
// an inspection-based rejection an admin can uphold days later, and clawing
// back means reversing a transfer whose funds may already have reached the
// seller's bank, on amounts far larger than a week's rent. So: at completion,
// when the rejection window has closed and the handover is recorded.
//
// Mirrors the rental path exactly — same ledger, same split, same escrow when
// the seller is not onboarded, same stable-key transfer.
func (h *PayoutHandler) SettleSalePayout(ctx context.Context, purchaseID, sellerID uuid.UUID, saleCents int64, note *string) {
	if saleCents <= 0 {
		h.logger.Info("sale payout: nothing to pay", "purchase_request_id", purchaseID)
		return
	}
	fee, sellerShare := models.ComputePayoutSplit(saleCents, h.feeBPS)

	accountID, status, aerr := h.payoutRepo.GetPayoutAccount(ctx, sellerID)
	rowStatus := models.PayoutPending
	if aerr != nil || accountID == nil || status != models.PayoutAccountReady {
		// Fails CLOSED, like rentals: a lookup error parks the money rather
		// than risking a transfer to an account we could not verify.
		rowStatus = models.PayoutAwaitingOnboarding
	}

	row, created, err := h.payoutRepo.CreateForSale(ctx, &models.OwnerPayout{
		PurchaseRequestID: &purchaseID,
		OwnerID:           sellerID,
		StripeAccountID:   accountID,
		GrossKeptCents:    saleCents,
		FeeBPS:            h.feeBPS,
		FeeCents:          fee,
		OwnerAmountCents:  sellerShare,
		Currency:          "USD",
		Status:            rowStatus,
		Source:            models.PayoutSourceSaleCompleted,
		Note:              note,
	})
	if err != nil {
		h.logger.Error("sale payout: create ledger row", "error", err, "purchase_request_id", purchaseID)
		return
	}
	if !created {
		if row != nil && row.GrossKeptCents != saleCents {
			h.logger.Error("sale payout: LEDGER MISMATCH — settlement disagrees with existing row",
				"purchase_request_id", purchaseID,
				"existing_gross_kept_cents", row.GrossKeptCents,
				"attempted_cents", saleCents, "existing_status", row.Status)
			if h.ticketRepo != nil {
				desc := fmt.Sprintf(
					"Sale payout row for purchase %s holds gross_kept %d¢ but completion recomputed %d¢ (existing status %q). Reconcile against Stripe before any further payout action.",
					purchaseID, row.GrossKeptCents, saleCents, row.Status)
				if _, terr := h.ticketRepo.CreateSystemTicket(ctx, sellerID, models.TicketCategoryPayments,
					"Sale payout ledger mismatch needs reconciliation", desc, nil, nil); terr != nil {
					h.logger.Error("sale payout: mismatch ticket", "error", terr)
				}
			}
			return
		}
		h.logger.Info("sale payout: ledger row already exists (idempotent)",
			"purchase_request_id", purchaseID, "status", row.Status)
		return
	}

	if rowStatus == models.PayoutAwaitingOnboarding {
		go h.notifHandler.Notify(sellerID, models.NotificationTypePayment,
			fmt.Sprintf("%s from your car sale is waiting", formatMoney(sellerShare)),
			fmt.Sprintf("Your sale completed and %s is ready. Finish payout setup in Earnings & payouts and it transfers automatically.", formatMoney(sellerShare)),
			nil, nil)
		return
	}
	h.executePayout(ctx, row)
}

// payoutKindWord keeps payout copy honest about what was paid for: a seller
// reading "your rental payout" after selling a car has been told the wrong
// thing about their own money.
func payoutKindWord(p *models.OwnerPayout) string {
	if p != nil && p.PurchaseRequestID != nil {
		return "sale"
	}
	return "rental"
}
