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
}

export interface AdminSupportMessage {
  id: string
  support_chat_id: string
  sender_id: string
  sender_kind: 'user' | 'admin'
  body: string
  created_at: string
}

// Stub types reserved for future migrations.
export interface AdminAccident {
  id: string
  driver_name?: string
  owner_name?: string
  car_title?: string
  created_at: string
}
export interface AdminCarSell {
  id: string
  driver_name?: string
  owner_name?: string
  car_title?: string
  created_at: string
}
