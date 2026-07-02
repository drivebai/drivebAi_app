// Shared response types — mirror the Go AdminRepository structs.
// Keep these aligned with backend/internal/repository/admin_repository.go.

export interface Page<T> {
  items: T[]
  total: number
  page: number
  limit: number
}

export interface AdminUser {
  id: string
  email: string
  role: 'driver' | 'car_owner' | 'admin'
  first_name: string
  last_name: string
  phone?: string | null
  is_email_verified: boolean
  onboarding_status: string
  is_blocked: boolean
  blocked_at?: string | null
  profile_photo_url?: string | null
  has_license: boolean
  has_registration: boolean
  created_at: string
}

export interface AdminCar {
  id: string
  title: string
  make: string
  model: string
  year: number
  owner_id?: string | null
  owner_email?: string | null
  owner_name?: string | null
  status: string
  is_paused: boolean
  is_approved: boolean
  is_for_rent: boolean
  is_for_sale: boolean
  weekly_rent_price?: number | null
  sale_price?: number | null
  currency: string
  address?: string | null
  cover_photo_url?: string | null
  created_at: string
}

export interface AdminCarPhoto {
  id: string
  slot_type: string
  file_url: string
}

export interface AdminCarDetail extends AdminCar {
  description?: string | null
  photos: AdminCarPhoto[]
}

export interface AdminChat {
  id: string
  car_id: string
  car_title: string
  car_year: number
  cover_photo_url?: string | null
  driver_id: string
  driver_name: string
  driver_email: string
  owner_id: string
  owner_name: string
  owner_email: string
  last_message_body?: string | null
  last_message_at?: string | null
}

export interface AdminMessage {
  id: string
  chat_id: string
  sender_id: string
  sender_name: string
  sender_kind: 'user' | 'admin'
  type: 'text' | 'system'
  body: string
  created_at: string
}

export interface AdminRent {
  id: string
  chat_id: string
  status: string
  weekly_price: number
  weeks: number
  currency: string
  driver_id: string
  driver_name: string
  driver_email: string
  owner_id: string
  owner_name: string
  owner_email: string
  car_id: string
  car_title: string
  car_year: number
  payment_intent_id?: string | null
  payment_status?: string | null
  start_date: string
  end_date?: string | null
  created_at: string

  // ---- Vehicle return fields (optional; backend may not populate yet) ----
  // Source of truth: vehicle_returns table joined onto the rental row.
  // Each field is independently optional so partial backend rollouts degrade
  // gracefully — UI hides anything that is null/undefined.
  return_id?: string | null
  return_status?:
    | 'driver_initiated'
    | 'owner_confirmed'
    | 'disputed'
    | 'completed'
    | 'cancelled'
    | null
  /** User who initiated the return (typically the driver). */
  return_initiated_by_id?: string | null
  return_initiated_by_name?: string | null
  return_initiated_by_email?: string | null
  /** Driver "I returned the car" timestamp. */
  return_driver_confirmed_at?: string | null
  /** Owner confirmation timestamp. */
  return_owner_confirmed_at?: string | null
  /** Final completed timestamp (refund settled or marked not_applicable). */
  return_completed_at?: string | null
  return_disputed_at?: string | null
  return_cancelled_at?: string | null
  return_used_days?: number | null
  return_unused_days?: number | null
  return_refund_amount_cents?: number | null
  return_refund_status?:
    | 'pending'
    | 'succeeded'
    | 'failed'
    | 'not_applicable'
    | null
  return_refund_id?: string | null
  return_refunded_at?: string | null
  return_refund_failure_reason?: string | null
  return_dispute_reason?: string | null
}

export interface AdminSupportChat {
  id: string
  user_id: string
  user_name: string
  user_email: string
  user_role: string
  user_photo_url?: string | null
  last_message_body?: string | null
  last_message_at?: string | null
  unread_count: number
}

export interface AdminSupportMessage {
  id: string
  support_chat_id: string
  sender_id: string
  sender_kind: 'user' | 'admin'
  body: string
  created_at: string
}

export interface AccidentAttachment {
  id: string
  accident_id: string
  slot: string
  file_url: string
  file_size: number
  mime_type: string
  created_at: string
}

export interface DriverInfo {
  driver_license_id?: string
  state_of_license?: string
  driver_name?: string
  address?: string
  city?: string
  state?: string
  zip?: string
  dob?: string
  people_in_vehicle?: string
  public_property_damaged?: string
  injuries?: string
  registrant_name?: string
  registrant_address?: string
  registrant_city?: string
  registrant_state?: string
  registrant_zip?: string
  plate_number?: string
  state_of_reg?: string
  vehicle_year_make?: string
  vehicle_type?: string
  ins_code?: string
}

export interface VehicleDamage {
  description?: string
  diagram?: number
}

export interface InsuranceInfo {
  insurance_company?: string
  vin?: string
  policy_number?: string
  policy_period_from?: string
  policy_period_to?: string
}

export interface OtherInfo {
  month?: string
  day?: string
  year?: string
  day_of_week?: string
  time?: string
  num_vehicles?: string
  num_injured?: string
  num_killed?: string
  police_investigated?: string
}

export interface AdminAccident {
  id: string
  reporter_id: string
  reporter_name: string
  reporter_email: string
  related_chat_id?: string
  related_car_id?: string
  car_title?: string
  status: 'draft' | 'submitted' | 'in_review' | 'resolved'
  driver1_info?: DriverInfo
  driver2_info?: DriverInfo
  vehicle_damage?: VehicleDamage
  accident_description?: string
  insurance_info?: InsuranceInfo
  other_info?: OtherInfo
  signature_url?: string
  signature_signed_at?: string
  submitted_at?: string
  attachments: AccidentAttachment[]
  created_at: string
  updated_at: string
}

export interface AdminAccidentsPage {
  items: AdminAccident[]
  total: number
  page: number
  limit: number
}
export interface AdminCarSell {
  id: string
  driver_name?: string
  owner_name?: string
  car_title?: string
  created_at: string
}

// ---------------------------------------------------------------------------
// Buy the Car (purchase) admin types
// Backend endpoints: /api/v1/admin/purchase-requests (list, detail),
// /api/v1/admin/purchase-rejections (queue),
// /api/v1/admin/purchase-rejections/{id}/resolve,
// /api/v1/admin/purchase-requests/{id}/retry-refund.
//
// These mirror the purchase_requests, purchase_bill_of_sales,
// purchase_rejections, and purchase_rejection_evidence tables described in
// the Buy the Car design spec. Every field is independently optional where
// the backend may not populate it yet — this lets the admin UI degrade
// gracefully during backend rollout.
// ---------------------------------------------------------------------------

/** Full purchase_request_status enum from the design spec. */
export type PurchaseRequestStatus =
  | 'requested'
  | 'accepted'
  | 'declined'
  | 'cancelled'
  | 'bos_pending_seller'
  | 'bos_pending_buyer'
  | 'bos_signed'
  | 'payment_authorized'
  | 'handover_scheduled'
  | 'awaiting_inspection'
  | 'inspection_accepted'
  | 'completed'
  | 'inspection_rejected'
  | 'rejected_refunded'
  | 'rejected_upheld'
  | 'expired'
  | 'expired_auth'

/** Payment status mirroring backend payment_status enum. */
export type PurchasePaymentStatus =
  | 'requires_payment_method'
  | 'requires_confirmation'
  | 'requires_action'
  | 'processing'
  | 'requires_capture'
  | 'authorized'
  | 'captured'
  | 'succeeded'
  | 'canceled'
  | 'refunded'
  | 'failed'
  | string

/** Refund status (matches vehicle_returns.refund_status). */
export type PurchaseRefundStatus =
  | 'pending'
  | 'succeeded'
  | 'failed'
  | 'not_applicable'
  | 'pending_manual'
  | string

export type PurchaseRejectionReason =
  | 'undisclosed_damage'
  | 'mechanical_issues'
  | 'title_or_paperwork'
  | 'vin_mismatch'
  | 'not_as_described'
  | 'no_show'
  | 'other'

export type PurchaseRejectionStatus =
  | 'submitted'
  | 'under_review'
  | 'accepted'
  | 'upheld'
  | 'withdrawn'

export interface PurchaseRejectionEvidence {
  id: string
  purchase_rejection_id: string
  /** Signed URL (already run through PrivateURLSigner) suitable for <img>/<video>/<a>. */
  file_url: string
  file_path?: string | null
  filename?: string | null
  mime_type: string
  size_bytes?: number | null
  created_at: string
}

export interface PurchaseRejection {
  id: string
  purchase_request_id: string
  reason_category: PurchaseRejectionReason | string
  explanation: string
  status: PurchaseRejectionStatus | string
  refund_status?: PurchaseRefundStatus | null
  admin_note?: string | null
  resolved_by?: string | null
  resolved_at?: string | null
  created_at: string
  updated_at: string
  evidence?: PurchaseRejectionEvidence[]
}

export interface PurchaseBillOfSale {
  id: string
  purchase_request_id: string

  // Vehicle block
  vehicle_year?: number | null
  vehicle_make?: string | null
  vehicle_model?: string | null
  vin?: string | null

  // Sale block
  sale_amount_cents?: number | null
  currency?: string | null
  terms_conditions?: string | null

  // Seller identity + signature
  seller_name?: string | null
  seller_address?: string | null
  /** Signed URL for the seller's signature PNG. */
  seller_signature_url?: string | null
  seller_signed_at?: string | null

  // Buyer identity + signature
  buyer_name?: string | null
  buyer_address?: string | null
  /** Signed URL for the buyer's signature PNG. */
  buyer_signature_url?: string | null
  buyer_signed_at?: string | null

  // Rendered artifact
  finalized_pdf_url?: string | null
  finalized_at?: string | null

  created_at: string
  updated_at: string
}

/**
 * Row shape returned by GET /admin/purchase-requests. The detail endpoint
 * (GET /admin/purchase-requests/{id}) additionally attaches `bill_of_sale`
 * and `rejection` (with `evidence`). Every joined display field
 * (car_title, buyer_email, ...) is treated as optional so the UI keeps
 * rendering if the backend list endpoint returns a slimmer projection.
 */
export interface PurchaseRequest {
  id: string
  car_id: string
  car_title?: string | null
  car_year?: number | null
  car_make?: string | null
  car_model?: string | null
  vin?: string | null
  cover_photo_url?: string | null

  seller_id: string
  seller_name?: string | null
  seller_email?: string | null

  buyer_id: string
  buyer_name?: string | null
  buyer_email?: string | null

  chat_id?: string | null

  offer_amount_cents: number
  currency: string
  buyer_message?: string | null

  status: PurchaseRequestStatus | string
  expires_at?: string | null
  auth_expires_at?: string | null

  handover_location?: string | null
  handover_latitude?: number | null
  handover_longitude?: number | null
  handover_scheduled_at?: string | null
  keys_handed_over_at?: string | null
  inspection_deadline_at?: string | null
  inspection_accepted_at?: string | null
  completed_at?: string | null

  payment_intent_id?: string | null
  payment_status?: PurchasePaymentStatus | null
  refund_status?: PurchaseRefundStatus | null
  refund_id?: string | null
  refunded_at?: string | null
  refund_failure_reason?: string | null

  /** Convenience roll-up so list tables can render a single "BoS" column. */
  bos_status?:
    | 'not_started'
    | 'pending_seller'
    | 'pending_buyer'
    | 'signed'
    | string
    | null

  cancellation_reason?: string | null

  created_at: string
  updated_at: string
}

export interface PurchaseRequestDetail extends PurchaseRequest {
  bill_of_sale?: PurchaseBillOfSale | null
  rejection?: PurchaseRejection | null
}
