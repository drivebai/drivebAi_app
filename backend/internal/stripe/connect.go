package stripe

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stripe/stripe-go/v81/webhook"
)

// Stripe Connect (v1 accounts with controller properties — the documented
// current path; the legacy Express/Standard/Custom types are deprecated for
// new platforms and Accounts v2 needs a preview API version our pin
// excludes).
//
// Money model: separate charges and transfers. The driver's PaymentIntent
// never carries transfer_data — the platform charges as itself, and the
// owner's share moves later via CreateTransfer when the rental settles.
// The old TODO about transfer_data[destination] is retired by design, not
// completed.

// connectAPIVersion matches the version the rest of this service sends.
// NOTE: this pins API *requests*; webhook *event* versions are pinned
// separately on the webhook endpoints themselves (2025-02-24.acacia).
const connectAPIVersion = "2024-04-10"

// postFormWithIdem is the shared raw-HTTP helper for the Connect methods:
// form-encoded POST with optional idempotency key, decoded into out.
func (s *Service) postFormWithIdem(path string, params url.Values, idempotencyKey string, out interface{}) error {
	req, err := http.NewRequest("POST", "https://api.stripe.com"+path, strings.NewReader(params.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", "Bearer "+s.secretKey)
	req.Header.Set("Stripe-Version", connectAPIVersion)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("stripe request %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("stripe error %d on %s: %s", resp.StatusCode, path, string(body))
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode %s response: %w", path, err)
		}
	}
	return nil
}

func (s *Service) getJSON(path string, out interface{}) error {
	req, err := http.NewRequest("GET", "https://api.stripe.com"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.secretKey)
	req.Header.Set("Stripe-Version", connectAPIVersion)
	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("stripe request %s: %w", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return fmt.Errorf("stripe error %d on %s: %s", resp.StatusCode, path, string(body))
	}
	return json.Unmarshal(body, out)
}

// ConnectedAccount mirrors the account fields the platform reads.
type ConnectedAccount struct {
	ID               string `json:"id"`
	DetailsSubmitted bool   `json:"details_submitted"`
	ChargesEnabled   bool   `json:"charges_enabled"`
	PayoutsEnabled   bool   `json:"payouts_enabled"`
	Requirements     struct {
		CurrentlyDue        []string `json:"currently_due"`
		EventuallyDue       []string `json:"eventually_due"`
		PastDue             []string `json:"past_due"`
		PendingVerification []string `json:"pending_verification"`
		DisabledReason      *string  `json:"disabled_reason"`
		CurrentDeadline     *int64   `json:"current_deadline"`
	} `json:"requirements"`
}

// CreateConnectedAccount creates the owner's connected account in the
// Express-equivalent controller configuration: Stripe collects KYC and
// keeps collecting when requirements change; the platform prices fees,
// carries loss liability, and controls payout timing; the owner gets the
// lightweight Express dashboard. Individuals, US-only (the cross-border
// boundary is drawn here on purpose). Only the `transfers` capability is
// requested — owners never process charges.
func (s *Service) CreateConnectedAccount(email, userID string) (*ConnectedAccount, error) {
	params := url.Values{}
	params.Set("country", "US")
	params.Set("email", email)
	params.Set("business_type", "individual")
	params.Set("controller[losses][payments]", "application")
	params.Set("controller[fees][payer]", "application")
	params.Set("controller[stripe_dashboard][type]", "express")
	params.Set("capabilities[transfers][requested]", "true")
	params.Set("metadata[user_id]", userID)

	var acct ConnectedAccount
	// Idempotent per user within a 5-minute bucket: a double-tap can't
	// mint two accounts, but a PERMANENTLY stable key would replay a
	// deleted/rejected account's id from Stripe's 24h idempotency cache
	// when the owner legitimately restarts setup (found in E2E testing —
	// the replayed dead account 400s every account_session after it).
	idemKey := fmt.Sprintf("connect-acct-%s-%d", userID, time.Now().Unix()/300)
	if err := s.postFormWithIdem("/v1/accounts", params, idemKey, &acct); err != nil {
		return nil, err
	}
	return &acct, nil
}

// GetConnectedAccount reads the current verification state.
func (s *Service) GetConnectedAccount(accountID string) (*ConnectedAccount, error) {
	var acct ConnectedAccount
	if err := s.getJSON("/v1/accounts/"+accountID, &acct); err != nil {
		return nil, err
	}
	return &acct, nil
}

// AccountSession is the client secret the StripeConnect iOS component
// consumes.
type AccountSession struct {
	ClientSecret string `json:"client_secret"`
}

// CreateAccountSession mints an account session enabling the embedded
// account-onboarding component (GA on iOS). Plain v1 endpoint — no server
// SDK required.
func (s *Service) CreateAccountSession(accountID string) (*AccountSession, error) {
	params := url.Values{}
	params.Set("account", accountID)
	params.Set("components[account_onboarding][enabled]", "true")

	var sess AccountSession
	if err := s.postFormWithIdem("/v1/account_sessions", params, "", &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

// Transfer is the owner-share movement record.
type Transfer struct {
	ID     string `json:"id"`
	Amount int64  `json:"amount"`
}

// CreateTransfer moves the owner's share from the platform balance to the
// connected account. source_transaction ties the transfer to the original
// charge (availability rides the charge's settlement; amount must not
// exceed the charge — always true here since owner_share ≤ kept ≤ charge).
// Callers pass a PER-ATTEMPT idempotency key and rely on
// FindTransferByGroup reconciliation for cross-attempt double-pay safety.
func (s *Service) CreateTransfer(accountID string, amountCents int64, currency, sourceChargeID, transferGroup, idempotencyKey string) (*Transfer, error) {
	params := url.Values{}
	params.Set("destination", accountID)
	params.Set("amount", fmt.Sprintf("%d", amountCents))
	params.Set("currency", strings.ToLower(currency))
	if sourceChargeID != "" {
		params.Set("source_transaction", sourceChargeID)
	}
	if transferGroup != "" {
		params.Set("transfer_group", transferGroup)
	}

	var tr Transfer
	if err := s.postFormWithIdem("/v1/transfers", params, idempotencyKey, &tr); err != nil {
		return nil, err
	}
	return &tr, nil
}

// FindTransferByGroup returns the transfer already created for a group, if
// any. The payout engine checks this BEFORE creating a transfer —
// reconciliation-first is what makes retries double-pay-safe (a stable
// idempotency key cannot be: Stripe caches ERROR responses under the key
// for 24h, so one transient 400 would poison every retry — hit in E2E via
// the payouts_enabled/transfers-capability activation race).
func (s *Service) FindTransferByGroup(transferGroup string) (*Transfer, error) {
	var list struct {
		Data []Transfer `json:"data"`
	}
	if err := s.getJSON("/v1/transfers?limit=1&transfer_group="+url.QueryEscape(transferGroup), &list); err != nil {
		return nil, err
	}
	if len(list.Data) == 0 {
		return nil, nil
	}
	return &list.Data[0], nil
}

// GetLatestChargeID resolves the charge behind a PaymentIntent, for
// source_transaction.
func (s *Service) GetLatestChargeID(paymentIntentID string) (string, error) {
	var pi struct {
		LatestCharge string `json:"latest_charge"`
	}
	if err := s.getJSON("/v1/payment_intents/"+paymentIntentID, &pi); err != nil {
		return "", err
	}
	return pi.LatestCharge, nil
}

// SetConnectWebhookSecret wires the SECOND webhook secret — Connect events
// (account.updated etc.) arrive on their own endpoint with its own signing
// secret. Setter, per the house pattern.
func (s *Service) SetConnectWebhookSecret(secret string) {
	s.connectWebhookSecret = secret
}

// VerifyConnectWebhookSignature verifies a Connect-endpoint event.
func (s *Service) VerifyConnectWebhookSignature(payload []byte, sigHeader string) error {
	if s.connectWebhookSecret == "" {
		return fmt.Errorf("connect webhook secret not configured")
	}
	_, err := webhook.ConstructEvent(payload, sigHeader, s.connectWebhookSecret)
	if err != nil {
		return fmt.Errorf("connect webhook signature verification: %w", err)
	}
	return nil
}
