# rentalsoftware

Multi-tenant Fleet & Vehicle Rental Management SaaS.

Three interfaces served from the same deployment:

- **Owner Admin** (`/owner`) — SaaS owner manages tenants, subscriptions, platform.
- **Manager** (`/app`) — rental company staff: fleet, bookings, customers, contracts.
- **Customer** (`/`) — public booking site and customer portal.

Stack: Go (chi + pgx) backend, PostgreSQL, React + Vite frontend, JWT auth, tenant-isolated at the database layer.

## Status

**Phase 1 (foundation) — in progress.** This repo currently contains:

- Working Go HTTP server with health check, auth, and tenant middleware.
- PostgreSQL schema for companies, users, roles, subscriptions, branches, vehicles (skeletons), and audit log.
- Password hashing (bcrypt), JWT-signed sessions, role-based access control.
- Tenant isolation enforced on every protected handler via `company_id` claim and DB-side `WHERE company_id = $1`.
- React + Vite frontend with route trees for the three interfaces, login/register, auth context.
- Dockerized Postgres via `docker-compose.yml`.

Phases 2–9 (billing, calendar, booking engine, contracts/e-sign, payments, maintenance, reporting, channel integrations, deploy hardening) are **not yet implemented**. See the project spec for the full scope.

## Quick start

### Prerequisites
- Go 1.22+
- Node 20+ and npm
- Docker (for Postgres) or a local Postgres 15+

### 1. Start Postgres

```bash
docker compose up -d db
```

Or point `DATABASE_URL` at your own Postgres.

### 2. Backend

```bash
cd backend
cp .env.example .env   # edit if needed
go mod tidy
go run ./cmd/server
```

Migrations run automatically on boot. API listens on `:8080`.

### 3. Frontend

```bash
cd frontend
npm install
npm run dev
```

Dev server on `:5173`, proxies `/api` to the Go backend.

### 4. Seed an owner account

```bash
curl -X POST http://localhost:8080/api/auth/bootstrap-owner \
  -H 'Content-Type: application/json' \
  -d '{"email":"owner@example.com","password":"ChangeMe123!","name":"Platform Owner"}'
```

This endpoint only succeeds while zero owner accounts exist; after that it 403s.

## Project layout

```
backend/
  cmd/server/           entrypoint
  internal/
    config/             env loading
    db/                 pgx pool + migrations runner
    auth/               password hashing, JWT
    middleware/         auth + tenant
    handlers/           HTTP handlers grouped by domain
    models/             DB row structs
  migrations/           *.up.sql / *.down.sql
frontend/
  src/
    api/                fetch client + typed endpoints
    auth/               AuthContext, protected routes
    apps/owner/         owner admin interface
    apps/manager/       rental company interface
    apps/customer/      public + portal
    pages/              shared top-level pages (login, landing)
```

## Security notes (current state)

- Passwords: bcrypt cost 12.
- JWT: HS256, 24h expiry; secret from `JWT_SECRET` env.
- Tenant isolation: every manager-scope endpoint resolves `company_id` from the JWT and includes it in SQL `WHERE` clauses. Owner-scope endpoints require `role=platform_owner`.
- CORS: dev-permissive; tighten before production.
- No raw card data touches this service. Payment provider (Stripe) is **not yet wired in**.
- Rate limiting, CSRF for cookie flows, webhook signature verification: **not yet implemented**.

## Deployment

Ubuntu / Nginx / CloudPanel instructions will be added in Phase 9. Do not run this against production data yet.
