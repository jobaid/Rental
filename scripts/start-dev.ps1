# Starts Postgres (via Docker if present, else expects a local 5432), backend, and frontend.
# Opens two new PowerShell windows for backend + frontend.

$ErrorActionPreference = "Stop"
$root = Split-Path -Parent $PSScriptRoot

Write-Host "== rentalsoftware dev start =="

# 1. Postgres
$havePg = $false
try {
    $probe = Test-NetConnection -ComputerName localhost -Port 5432 -WarningAction SilentlyContinue
    if ($probe.TcpTestSucceeded) { $havePg = $true }
} catch {}

if (-not $havePg) {
    $dockerCmd = Get-Command docker -ErrorAction SilentlyContinue
    if ($dockerCmd) {
        Write-Host "Starting Postgres via docker compose..."
        Push-Location $root
        docker compose up -d db
        Pop-Location
        Start-Sleep -Seconds 5
    } else {
        Write-Error @"
Postgres is NOT running on localhost:5432 and Docker is not installed.
Install ONE of:
  - PostgreSQL 15+ (winget install PostgreSQL.PostgreSQL.16)
    Then create the DB:
      psql -U postgres -c "CREATE USER rental WITH PASSWORD 'rental';"
      psql -U postgres -c "CREATE DATABASE rental OWNER rental;"
  - Docker Desktop (winget install Docker.DockerDesktop) and re-run this script.
"@
        exit 1
    }
}

Write-Host "Postgres is reachable on localhost:5432."

# 2. Backend
Start-Process -FilePath "powershell.exe" -ArgumentList @(
  "-NoExit", "-Command",
  "cd '$root\backend'; Write-Host 'Starting backend...'; go run ./cmd/server"
)

# 3. Frontend
Start-Process -FilePath "powershell.exe" -ArgumentList @(
  "-NoExit", "-Command",
  "cd '$root\frontend'; Write-Host 'Starting frontend...'; npm run dev"
)

Write-Host ""
Write-Host "Backend  : http://localhost:8080/healthz"
Write-Host "Frontend : http://localhost:5173"
Write-Host "First-time setup: open http://localhost:5173/setup to create the platform owner."
