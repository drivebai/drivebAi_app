# DriveBai Admin Panel

Vue 3 + Vite + TypeScript SPA that talks to the existing Go backend (no direct DB access).

## Stack

- Vue 3 (`<script setup>` + Composition API)
- Vite + TypeScript
- Pinia (auth store + toasts)
- Vue Router 4 (history mode, mounted at `/admin/`)
- Plain CSS — no UI library, styled to match the prototype

## Prerequisites

- Node 18+ (tested on Node 20)
- Backend running locally on `:8080` with admin migrations applied (see below)

## Run locally

```bash
# 1. Install deps
cd admin
npm install

# 2. Configure env
cp .env.example .env
# edit VITE_API_BASE_URL if your backend isn't on http://localhost:8080

# 3. Start dev server (Vite proxies /api and /uploads to the backend)
npm run dev
# → http://localhost:5173/admin/
```

## Build

```bash
npm run build      # type-check + bundle to dist/
npm run preview    # serve the built dist/
```

The build output (`dist/`) is a static site you can drop behind any web server.
Since the SPA uses history mode, configure your host to fall back to `index.html` for unknown paths under `/admin/`.

## Environment variables

| Var                  | Purpose                                                | Default                  |
|----------------------|--------------------------------------------------------|--------------------------|
| `VITE_API_BASE_URL`  | Where the dev proxy sends `/api` and `/uploads`        | `http://localhost:8080`  |

In production the SPA calls the API on the same origin, so deploy the admin behind the same domain as the API (e.g. `/admin/` on `drivebai-api.fly.dev`) or expose a reverse proxy that forwards `/api` to the backend.

## Auth

The admin reuses the existing JWT login flow (`POST /api/v1/auth/login`).
- The login screen rejects any account whose `role !== "admin"`.
- All `/api/v1/admin/*` routes are additionally guarded server-side by `RequireRole(admin)`.

To create the first admin (one-time):

```sql
-- in the backend's psql:
UPDATE users SET role = 'admin' WHERE email = 'you@drivebai.com';
```

Then sign in at `/admin/login`.

## Backend prerequisites

Run the new migration (also creates `cars.is_approved`, `users.is_blocked`, `support_chats`, `support_messages`):

```bash
cd backend
make migrate-up   # or: psql $DATABASE_URL -f migrations/000015_add_admin_fields.up.sql
```

After this:
- New car listings default to `is_approved = false` and **won't appear in Discover** until an admin toggles them on in the Vehicles page.
- Existing cars are auto-approved by the migration so iOS users see no change.
- Blocked users can no longer log in (`/auth/login` returns `403 ACCOUNT_BLOCKED`).

## Project layout

```
admin/
  index.html
  vite.config.ts
  src/
    main.ts            # bootstraps Pinia + router
    App.vue            # mounts <RouterView/> + global toasts
    router/            # routes + admin auth guard
    api/               # fetch client + typed adminApi surface
    stores/            # Pinia: auth, toasts
    layouts/           # AdminLayout (sidebar + main)
    components/        # DataTable, Drawer, ConfirmDialog, Toggle, etc.
    pages/             # Login, Users, Vehicles, Chats, Rents, Support, Accidents, CarSell
    utils/format.ts    # date / currency / image-url helpers
    styles/main.css    # global tokens + table styles
```

## Pages — what each one does

| Route        | Backend route                                  | Behavior                                                                                                          |
|--------------|------------------------------------------------|-------------------------------------------------------------------------------------------------------------------|
| `/users`     | `GET /admin/users`, `PATCH /users/:id/block`   | search by email/name, role + status filters, block/unblock with confirm, drawer details + driver doc summary       |
| `/vehicles`  | `GET /admin/cars`, `PATCH /cars/:id/approve`   | search, approval toggle (confirms before hiding), drawer with photo grid + price/location                          |
| `/chats`     | `GET /admin/chats`, `GET /chats/:id/messages`  | left list of request chats with last-message preview, right pane shows full message history (read-only)            |
| `/rents`     | `GET /admin/rents`, `GET /rents/:id`           | active/finished pills, search, drawer shows weekly price + total + Stripe intent + payment status                  |
| `/support`   | `GET /admin/support/...`, `POST .../messages`  | left list of users contacting support, right pane is a real two-way conversation — admin can type and reply        |
| `/accidents` | `GET /admin/accidents` (stub)                  | UI ready; backend currently returns empty pages until the accident-report schema lands                             |
| `/car-sell`  | `GET /admin/car-sells` (stub)                  | UI ready, including the prototype's two-form (Driver / Seller) inline layout                                       |

## Notes

- Browsers never talk to Postgres. Every operation is a JSON call to a Go handler.
- `RequireAdmin` is just `RequireRole(models.RoleAdmin)` — the existing middleware already in the codebase.
- Toggling a vehicle's approval to OFF triggers a `ConfirmDialog` because it removes a listing from Discover for all drivers.
- Blocking a user triggers a `ConfirmDialog` because it forces them out of the app.
