# One-shot DB setup. RUN AS ADMINISTRATOR.
# - Flips pg_hba.conf to 'trust' temporarily
# - Resets the 'postgres' password to 'postgres'
# - Creates the 'rental' database
# - Installs required extensions
# - Restores the original pg_hba.conf
#
# Idempotent: safe to run twice.

$ErrorActionPreference = "Stop"

# Find the Postgres install.
$pgRoot = Get-ChildItem "C:\Program Files\PostgreSQL" -Directory -ErrorAction SilentlyContinue | Sort-Object Name -Descending | Select-Object -First 1
if (-not $pgRoot) { throw "PostgreSQL not found under C:\Program Files\PostgreSQL" }
$pgData = Join-Path $pgRoot.FullName "data"
$pgBin  = Join-Path $pgRoot.FullName "bin"
$svc    = (Get-Service -Name "postgresql-*" | Select-Object -First 1).Name
if (-not $svc) { throw "No postgresql-* Windows service found" }

Write-Host "Postgres root: $($pgRoot.FullName)"
Write-Host "Service:       $svc"

# 1. Backup pg_hba, flip to trust auth for localhost.
$hba = Join-Path $pgData "pg_hba.conf"
if (-not (Test-Path $hba)) { throw "pg_hba.conf not found at $hba" }
Copy-Item $hba "$hba.bak" -Force
(Get-Content $hba) `
  -replace '^(host\s+all\s+all\s+127\.0\.0\.1/32\s+)\S+', '$1trust' `
  -replace '^(host\s+all\s+all\s+::1/128\s+)\S+',       '$1trust' |
  Set-Content $hba

Restart-Service $svc
Start-Sleep -Seconds 2

# 2. Reset postgres password, create db, add extensions.
$env:Path = "$pgBin;$env:Path"
& psql -U postgres -d postgres -c "ALTER USER postgres WITH PASSWORD 'postgres';"
# CREATE DATABASE isn't transactional; swallow 'already exists'.
try { & psql -U postgres -d postgres -c "CREATE DATABASE rental;" } catch { Write-Host "rental db may already exist" }
& psql -U postgres -d rental -c "CREATE EXTENSION IF NOT EXISTS pgcrypto;"
& psql -U postgres -d rental -c "CREATE EXTENSION IF NOT EXISTS btree_gist;"

# 3. Restore secure auth.
Move-Item "$hba.bak" $hba -Force
Restart-Service $svc

Write-Host ""
Write-Host "=== DONE ==="
Write-Host "User:     postgres"
Write-Host "Password: postgres"
Write-Host "Database: rental"
Write-Host ""
Write-Host "Next: open a NORMAL PowerShell and run:"
Write-Host "  cd 'C:\Users\MD JOBAID AZIM\rentalsoftware\backend'"
Write-Host "  go run ./cmd/server"
