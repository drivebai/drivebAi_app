// Admin API surface. One typed function per backend route.
// All paths go through the Go API; never query the DB directly.

import { api, qs } from './client'
import type {
  AdminUser, AdminCar, AdminCarDetail, AdminChat, AdminMessage,
  AdminRent, AdminSupportChat, AdminSupportMessage, AdminAccident, AdminCarSell, Page,
} from './types'

const BASE = '/api/v1/admin'

export const adminApi = {
  // ---- Users ----
  listUsers: (q: { query?: string; role?: string; status?: string; page?: number; limit?: number }) =>
    api.get<Page<AdminUser>>(`${BASE}/users${qs(q)}`),
  getUser: (id: string) => api.get<AdminUser>(`${BASE}/users/${id}`),
  blockUser: (id: string, blocked: boolean) =>
    api.patch<{ ok: boolean; blocked: boolean }>(`${BASE}/users/${id}/block`, { blocked }),

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

  // ---- Accidents / Car sell (stubs until tables exist) ----
  listAccidents: () => api.get<Page<AdminAccident>>(`${BASE}/accidents`),
  listCarSells: () => api.get<Page<AdminCarSell>>(`${BASE}/car-sells`),
}
