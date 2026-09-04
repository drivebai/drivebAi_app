package handlers

// Audit H6: the ONE way a handler resolves a user's Stripe customer.
//
// The old FindOrCreateCustomer searched Stripe BY EMAIL. Account deletion
// tombstones the row and frees the email for re-registration — while the
// Stripe customer keyed to that email, saved cards included, lived on. A
// later registrant with the same address was then handed the previous
// person's cards in PaymentSheet. The binding is now users.stripe_customer_id
// (backfilled in 000052); email never selects a customer again.

import (
	"context"
	"log/slog"

	"github.com/drivebai/backend/internal/models"
	stripeService "github.com/drivebai/backend/internal/stripe"
	"github.com/google/uuid"
)

// stripeCustomerStore is the narrow slice of UserRepository this needs.
type stripeCustomerStore interface {
	GetStripeCustomerID(ctx context.Context, userID uuid.UUID) (*string, error)
	SetStripeCustomerID(ctx context.Context, userID uuid.UUID, customerID string) error
}

// customerForUser returns the user's bound Stripe customer, verifying it
// still exists at Stripe, or mints and binds a fresh one. Concurrent calls
// for the same user may transiently create an extra customer object at
// Stripe (harmless — both belong to this user; last bind wins); a customer
// can never be resolved from another user's row.
func customerForUser(ctx context.Context, svc *stripeService.Service, store stripeCustomerStore, user *models.User, logger *slog.Logger) (*stripeService.Customer, error) {
	if cid, err := store.GetStripeCustomerID(ctx, user.ID); err == nil && cid != nil && *cid != "" {
		c, gerr := svc.GetCustomer(*cid)
		if gerr != nil {
			// Transient Stripe failure: do NOT fall through to create — a
			// flake must not mint duplicate customers.
			return nil, gerr
		}
		if c != nil {
			return c, nil
		}
		// (nil, nil): the bound customer was deleted at Stripe — re-mint.
	}

	c, err := svc.CreateCustomer(user.Email, user.FullName(), user.ID.String())
	if err != nil {
		return nil, err
	}
	if serr := store.SetStripeCustomerID(ctx, user.ID, c.ID); serr != nil {
		// The customer is valid for THIS charge either way; an unbound row
		// just re-mints on the next payment (mild Stripe-side duplication,
		// never a cross-user leak). Log, don't fail the payment.
		logger.Error("stripe customer: bind failed", "error", serr, "user_id", user.ID, "customer_id", c.ID)
	}
	return c, nil
}
