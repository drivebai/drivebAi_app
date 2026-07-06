// Admin API surface. One typed function per backend route.
// All paths go through the Go API; never query the DB directly.

import { api, qs } from './client'
import type {
  AdminUser, AdminCar, AdminCarDetail, AdminChat, AdminMessage,
  AdminRent, AdminSupportChat, AdminSupportMessage, AdminAccident, AdminAccidentsPage, AdminCarSell, Page,
  PurchaseRequest, PurchaseRequestDetail, PurchaseRejection,
} from './types'

const BASE = '/api/v1/admin'

export const adminApi = {
  // ---- Users ----
  listUsers: (q: { query?: string; role?: string; status?: string; page?: number; limit?: number }) =>
    api.get<Page<AdminUser>>(`${BASE}/users${qs(q)}`),
  getUser: (id: string) => api.get<AdminUser>(`${BASE}/users/${id}`),
  blockUser: (id: string, blocked: boolean) =>
    api.patch<{ ok: boolean; blocked: boolean }>(`${BASE}/users/${id}/block`, { blocked }),

  /**
   * Admin edit of safe profile fields (first_name, last_name, phone).
   * Email, role, verification flags, and password are deliberately NOT
   * editable here — see backend AdminHandler.UpdateUserProfile for the
   * rationale. Omitted fields mean "leave unchanged" on the server.
   * Returns the refreshed AdminUser so the table row can be swapped
   * without a separate fetch.
   */
  updateUserProfile: (
    id: string,
    body: { first_name?: string; last_name?: string; phone?: string },
  ) => api.patch<AdminUser>(`${BASE}/users/${id}/profile`, body),

  /**
   * Admin-triggered password reset. Passwords are one-way bcrypt hashes —
   * there is nothing to "view" or "set" here by design. The backend reuses
   * the ForgotPassword internals: invalidates old tokens, stores a hashed
   * one-time token, and emails the user a reset link. Responds 202; the
   * token is never returned to the admin.
   */
  resetUserPassword: (id: string) =>
    api.post<{ message?: string }>(`${BASE}/users/${id}/reset-password`, {}),

  // ---- Cars ----
  listCars: (q: { query?: string; page?: number; limit?: number }) =>
    api.get<Page<AdminCar>>(`${BASE}/cars${qs(q)}`),
  getCar: (id: string) => api.get<AdminCarDetail>(`${BASE}/cars/${id}`),
  approveCar: (id: string, isApproved: boolean) =>
    api.patch<{ ok: boolean; is_approved: boolean }>(`${BASE}/cars/${id}/approve`, { is_approved: isApproved }),

  // ---- Chats (driver↔owner) ----
  listChats: (q: { query?: string; page?: number; limit?: number }) =>
    api.get<Page<AdminChat>>(`${BASE}/chats${qs(q)}`),
  listChatMessages: (chatId: string) =>
    api.get<{ messages: AdminMessage[] }>(`${BASE}/chats/${chatId}/messages`),
  sendChatMessage: (chatId: string, text: string) =>
    api.post<AdminMessage>(`${BASE}/chats/${chatId}/messages`, { text }),

  // ---- Rents ----
  listRents: (q: { query?: string; status?: string; page?: number; limit?: number }) =>
    api.get<Page<AdminRent>>(`${BASE}/rents${qs(q)}`),
  getRent: (id: string) => api.get<AdminRent>(`${BASE}/rents/${id}`),

  // ---- Support ----
  listSupportChats: () => api.get<{ chats: AdminSupportChat[] }>(`${BASE}/support/chats`),
  listSupportMessages: (id: string) =>
    api.get<{ messages: AdminSupportMessage[] }>(`${BASE}/support/chats/${id}/messages`),
  sendSupportMessage: (id: string, body: string) =>
    api.post<AdminSupportMessage>(`${BASE}/support/chats/${id}/messages`, { body }),
  markSupportRead: (id: string) =>
    api.post<{ ok: boolean }>(`${BASE}/support/chats/${id}/read`, {}),

  // ---- Accidents / Car sell ----
  listAccidents: (q: { status?: string; page?: number; limit?: number }) =>
    api.get<AdminAccidentsPage>(`${BASE}/accidents${qs(q)}`),
  getAccident: (id: string) => api.get<AdminAccident>(`${BASE}/accidents/${id}`),
  updateAccidentStatus: (id: string, status: string) =>
    api.patch<{ ok: boolean }>(`${BASE}/accidents/${id}/status`, { status }),
  listCarSells: () => api.get<Page<AdminCarSell>>(`${BASE}/car-sells`),

  // ---- Buy the Car (purchase requests) ----
  // Paths per the Buy the Car spec. Backend may not have shipped these yet;
  // the page catches the resulting 404/500 and shows an empty state.
  listPurchaseRequests: (q: { query?: string; status?: string; page?: number; limit?: number }) =>
    api.get<Page<PurchaseRequest>>(`${BASE}/purchase-requests${qs(q)}`),
  getPurchaseRequest: (id: string) =>
    api.get<PurchaseRequestDetail>(`${BASE}/purchase-requests/${id}`),
  listPurchaseRejections: (q: { status?: string; page?: number; limit?: number }) =>
    api.get<Page<PurchaseRejection>>(`${BASE}/purchase-rejections${qs(q)}`),
  resolvePurchaseRejection: (id: string, body: { resolution: 'accept' | 'uphold'; note?: string }) =>
    api.post<PurchaseRejection>(`${BASE}/purchase-rejections/${id}/resolve`, body),
  retryPurchaseRefund: (id: string) =>
    api.post<{ ok: boolean }>(`${BASE}/purchase-requests/${id}/retry-refund`, {}),
}
