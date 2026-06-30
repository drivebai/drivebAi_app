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
