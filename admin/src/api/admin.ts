// Admin API surface. One typed function per backend route.
// All paths go through the Go API; never query the DB directly.

import { api, qs } from './client'
import type {
  AdminUser, AdminUserDocument, AdminCar, AdminCarDetail, AdminCarDocument, AdminChat, AdminMessage,
  AdminRent, AdminSupportChat, AdminSupportMessage, AdminAccident, AdminAccidentsPage, AdminCarSell, Page,
  AdminTicket, AdminTicketsPage,
  PurchaseRequest, PurchaseRequestDetail, PurchaseRejection, PurchaseBillOfSale,
} from './types'

const BASE = '/api/v1/admin'

export const adminApi = {
  // ---- Users ----
  listUsers: (q: { query?: string; role?: string; status?: string; page?: number; limit?: number }) =>
    api.get<Page<AdminUser>>(`${BASE}/users${qs(q)}`),
  getUser: (id: string) => api.get<AdminUser>(`${BASE}/users/${id}`),
  blockUser: (id: string, blocked: boolean) =>
    api.patch<{ ok: boolean; blocked: boolean }>(`${BASE}/users/${id}/block`, { blocked }),
  // Soft delete (batch item 3): anonymize-in-place — frees email + phone,
  // blocks the account, keeps transaction history. Never a hard row delete.
  deleteUser: (id: string) => api.del<{ ok: boolean }>(`${BASE}/users/${id}`),

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
  // Driver-document replacement on the user's behalf (client points 3+4).
  // Both calls hit the same endpoint; the replaced slot resets to pending
  // review and the user is notified.
  replaceUserDocument: (userId: string, documentType: string, file: File) => {
    const form = new FormData()
    form.append('file', file)
    form.append('document_type', documentType)
    return api.postForm<AdminUserDocument>(`${BASE}/users/${userId}/documents`, form)
  },
  applyChatAttachmentToUser: (userId: string, attachmentId: string, documentType: string) =>
    api.post<AdminUserDocument>(`${BASE}/users/${userId}/documents`, {
      support_attachment_id: attachmentId,
      document_type: documentType,
    }),

  /**
   * Admin-triggered password reset. Passwords are one-way bcrypt hashes —
   * there is nothing to "view" or "set" here by design. The backend reuses
   * the ForgotPassword internals: invalidates old tokens, stores a hashed
   * one-time token, and emails the user a reset link. Responds 202; the
   * token is never returned to the admin.
   */
  resetUserPassword: (id: string) =>
    api.post<{ message?: string }>(`${BASE}/users/${id}/reset-password`, {}),

  /**
   * Admin-initiated live chat: open (or create) the user's ONE support
   * chat — the same thread as their in-app Ask-for-Help button. Returns
   * the chat id for preselecting it on the Support page.
   */
  openSupportChat: (id: string) =>
    api.post<{ chat_id: string; user_id: string }>(`${BASE}/users/${id}/support-chat`, {}),

  /**
   * Switch the user's CURRENT mode (driver/owner). Creates the target
   * profile if the user never made one; 409 DRIVER_DOCS_REQUIRED when
   * switching to driver without a valid license on file. The user's
   * device is updated via WebSocket + push.
   */
  setUserActiveProfile: (id: string, role: 'driver' | 'car_owner') =>
    api.patch<{ ok: boolean; active_role: string }>(`${BASE}/users/${id}/active-profile`, { role }),

  /**
   * Personal documents (driver's license + optional docs) with signed,
   * short-lived file URLs for inline viewing.
   */
  listUserDocuments: (id: string) =>
    api.get<AdminUserDocument[]>(`${BASE}/users/${id}/documents`),

  /**
   * Approve or decline a user's document. Declining REQUIRES a reason —
   * the backend rejects an empty one — and notifies the user with it.
   * A rejected driver's license stops counting as the required driver
   * document until a new copy is uploaded.
   */
  setUserDocumentStatus: (
    userId: string,
    docId: string,
    body: { status: 'verified' | 'rejected'; reason?: string },
  ) => api.patch<{ ok: boolean; id: string; status: string }>(
    `${BASE}/users/${userId}/documents/${docId}/status`, body),

  // ---- Cars ----
  listCars: (q: { query?: string; page?: number; limit?: number }) =>
    api.get<Page<AdminCar>>(`${BASE}/cars${qs(q)}`),
  getCar: (id: string) => api.get<AdminCarDetail>(`${BASE}/cars/${id}`),
  // Car-document replacement on the owner's behalf (batch item 1). Both
  // calls hit the same endpoint; it replaces every older document of the
  // same type and notifies the owner.
  replaceCarDocument: (carId: string, documentType: string, file: File) => {
    const form = new FormData()
    form.append('file', file)
    form.append('document_type', documentType)
    return api.postForm<AdminCarDocument>(`${BASE}/cars/${carId}/documents`, form)
  },
  applyChatAttachmentToCar: (carId: string, attachmentId: string, documentType: string) =>
    api.post<AdminCarDocument>(`${BASE}/cars/${carId}/documents`, {
      support_attachment_id: attachmentId,
      document_type: documentType,
    }),
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
  // Attachment upload rides the SHARED support endpoint (not /admin): the
  // handler is owner-or-admin and posts an admin token's upload with
  // sender_kind 'admin', fanning out WS + push to the user. Images, PDFs,
  // and MP4/QuickTime video; 50 MB server cap.
  uploadSupportAttachment: (chatId: string, file: File, caption?: string) => {
    const form = new FormData()
    form.append('file', file)
    if (caption) form.append('body', caption)
    return api.postForm<AdminSupportMessage>(`/api/v1/support/chats/${chatId}/attachments`, form)
  },
  markSupportRead: (id: string) =>
    api.post<{ ok: boolean }>(`${BASE}/support/chats/${id}/read`, {}),

  // ---- Support tickets (structured requests) ----
  // Drafts are never returned; the queue is oldest-submitted-first server-side.
  // Status may be '' (all non-draft), 'open', 'resolved', or 'closed'.
  listTickets: (q: { status?: string; page?: number; limit?: number }) =>
    api.get<AdminTicketsPage>(`${BASE}/tickets${qs(q)}`),
  getTicket: (id: string) => api.get<AdminTicket>(`${BASE}/tickets/${id}`),
  updateTicketStatus: (id: string, status: 'open' | 'resolved' | 'closed') =>
    api.patch<{ ok: boolean }>(`${BASE}/tickets/${id}/status`, { status }),

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

  /**
   * Regenerate the finalized Bill of Sale PDF (Design A §3d).
   *
   * NOTE: this route lives in the *shared* protected group, NOT under
   * `${BASE}` (/api/v1/admin). Per the spec it is callable by the two
   * participants AND by admins via the existing admin middleware, so the
   * admin's Bearer token is accepted. It is idempotent: when
   * `finalized_pdf_url` is NULL (and the BoS is signed) it regenerates the
   * deterministic PDF and returns the freshly-signed BoS; when already
   * finalized it is a no-op and returns the existing signed response.
   *
   * The backend may not have shipped this endpoint yet — callers should
   * catch the 404/403/422 and surface a toast rather than assume success.
   */
  finalizeBillOfSale: (id: string) =>
    api.post<PurchaseBillOfSale>(`/api/v1/purchase-requests/${id}/bos/finalize`, {}),
}
