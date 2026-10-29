# rentalsoftware

Multi-tenant Fleet & Vehicle Rental Management SaaS.

Three interfaces served from the same deployment:

- **Owner Admin** (`/owner`) — platform owner manages tenants, subscriptions.
- **Manager** (`/app`) — rental company staff: fleet, bookings, customers, calendar, contracts, payments, maintenance, reports.
- **Customer** (`/`) — public booking site, booking engine, customer portal, e-signature page.

Stack: Go (chi + pgx) backend, PostgreSQL (with `btree_gist` for overlap exclusion), React + Vite frontend, JWT auth, tenant-isolated at the database layer.

## Status

- **Phase 1** auth, tenants, roles, companies, branches, fleet — **done**
- **Phase 2** owner admin, plans, company provisioning — **done** (plan editing UI pending)
- **Phase 3** customers, employees, branches — **done**
- **Phase 4** reservation calendar, availability engine, booking lifecycle, checkout/return — **done** (DB-level overlap exclusion, status transitions, vehicle status side-effects, availability check endpoint, 14-day timeline calendar)
- **Phase 5** customer booking engine + portal + guest lookup + sign page — **done**
- **Phase 6** rental contracts with signing links & e-signature, manual payments, receipts — **done** (Stripe wiring deferred; see below)
- **Phase 7** maintenance records, vehicle blocks, reports, notifications log — **done** (email delivery is just a `notifications` queue table; wire your SMTP in Phase 7+)
- **Phase 8** channel integrations (Expedia, KAYAK, Turo) — **not implemented**: these require partner API access and credentials
- **Phase 9** production hardening, backups, deploy — **not implemented**

## Prerequisites

- **Go 1.22+** (installed — `go1.27.1` detected)
- **Node 20+** (installed — `v24.19.0` detected)
- **PostgreSQL 15+** — ⚠ not installed. Pick ONE:
  - `winget install -e --id PostgreSQL.PostgreSQL.16` and then (as Postgres superuser):
    ```sh
    psql -U postgres -c "CREATE USER rental WITH PASSWORD 'rental';"
    psql -U postgres -c "CREATE DATABASE rental OWNER rental;"
    ```
  - `winget install -e --id Docker.DockerDesktop` → start Docker Desktop → `docker compose up -d db`.

## Quick start (Windows / PowerShell)

```powershell
# 1. One-shot script (checks Postgres, launches backend + frontend)
.\scripts\start-dev.ps1
```

Or step-by-step:

```powershell
# Backend
cd backend
go mod tidy
go run ./cmd/server      # http://localhost:8080

# Frontend (new terminal)
cd frontend
npm install
npm run dev              # http://localhost:5173
```

Migrations run automatically on backend boot. Open `http://localhost:5173/setup` to create the first platform owner.

## Walkthrough

1. **Create owner** → `/setup` (works only until the first owner exists; afterwards 403s).
2. **Sign in as owner** → `/owner` → Companies → create a tenant and its first company_owner.
3. **Sign in as company_owner** → use the company slug + credentials you just set → `/app`.
4. **Add vehicles, branches, customers**, then **create a reservation** at `/app/bookings`.
5. The overlapping-reservation guard is at the database layer (`EXCLUDE USING gist`), so even concurrent inserts can't double-book.
6. From a booking detail page, generate a signing link; open it in an incognito tab to sign.
7. The public booking engine lives at `/search` → `/book`. Guests who set a password get a portal account at `/portal`.

## API surface (what Phase 4-7 added)

Public:
- `GET  /api/public/companies`
- `GET  /api/public/search?company=&from=&to=` RFC3339 dates
- `POST /api/public/book`
- `GET  /api/public/booking/{number}?email=`
- `GET  /api/public/contract/{token}`
- `POST /api/public/contract/{token}/sign`

Manager (tenant-scoped, JWT required, roles enforced):
- `GET /api/manager/stats`, `GET /api/manager/reports`
- CRUD-ish on `/manager/branches`, `/manager/vehicles`, `/manager/customers`
- `/manager/reservations` list/create/get/patch, `/manager/availability`
- `/manager/payments`, `/manager/contracts`
- `/manager/blocks`, `/manager/maintenance`

Portal (role: customer):
- `GET /api/portal/bookings`

## What is **not** real yet (be honest with the user)

- **Stripe / payment gateway** — the `payments` table and `/manager/payments` endpoint record manual payments. There is no Stripe PaymentIntent flow or webhook verifier yet.
- **Email delivery** — notifications are inserted into a `notifications` queue table with status `queued`; no SMTP sender runs. Wire an SMTP/SES/SendGrid worker to drain the queue.
- **E-signature legal compliance** — the signing flow captures name + a signature string and timestamps it. Audit trail is minimal (no IP/UA capture, no PDF archive). Validate against your jurisdiction before claiming compliance.
- **Channel integrations (Expedia / KAYAK / Turo)** — require partner approval and credentials. Not implemented.
- **CSV/PDF exports** — Reports page exports a basic CSV; no PDF yet.
- **Rate limiting, CSRF, webhook signature verification, backup/restore** — Phase 9.

## Project layout

```
backend/
  cmd/server/           entrypoint, routes
  internal/
    auth/               bcrypt + JWT
    config/             env loading
    db/                 pgx pool + embedded migrations
      migrations/
        0001_init.up.sql
        0002_reservations.up.sql           (btree_gist overlap exclusion)
        0003_booking_lifecycle.up.sql      (payments, contracts, maintenance, notifications)
    handlers/
      auth.go           login / bootstrap / me
      owner.go          platform-owner endpoints
      tenant.go         branches, vehicles, manager stats
      customers.go      customer CRUD
      reservations.go   reservation lifecycle, payments, contracts
      maintenance.go    blocks, maintenance, reports
      public.go         booking engine + portal
    middleware/auth.go  JWT, role, tenant guards
frontend/
  src/
    api/                fetch client
    auth/               AuthContext
    apps/owner/         SaaS admin
    apps/manager/       rental company
    apps/customer/      search, book, portal, sign + landing shell
    pages/              login, setup, landing
```

## Security notes

- bcrypt cost 12. JWT HS256, 24h expiry.
- Every manager/portal endpoint checks `company_id` from the JWT; SQL always includes `WHERE company_id = $1`.
- Overlap and block exclusion are **database constraints** (`EXCLUDE USING gist`), not application logic — concurrent inserts are safe.
- No raw card data touches this service.
- CORS is dev-permissive. Rate-limit middleware is not wired yet.

## Deployment

Phase 9 — Ubuntu / Nginx / CloudPanel instructions pending. Do not run against production data yet.
