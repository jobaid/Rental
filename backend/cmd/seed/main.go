// Dev seeder: inserts random fleet, customers, bookings and maintenance for one company.
//
// Usage (PowerShell):
//   cd backend
//   go run ./cmd/seed -company acme
//
// Reads DATABASE_URL from backend/.env (same file the server uses).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

var (
	makes = []string{"Toyota", "Honda", "Ford", "Hyundai", "Kia", "Volkswagen", "Mazda", "Nissan", "Chevrolet", "BMW"}
	models = map[string][]string{
		"Toyota":     {"Corolla", "Camry", "RAV4", "Yaris", "Hilux"},
		"Honda":      {"Civic", "Accord", "CR-V", "Fit", "HR-V"},
		"Ford":       {"Focus", "Fiesta", "Escape", "Explorer", "F-150"},
		"Hyundai":    {"Elantra", "Sonata", "Tucson", "i20", "Kona"},
		"Kia":        {"Rio", "Cerato", "Sportage", "Picanto", "Sorento"},
		"Volkswagen": {"Golf", "Polo", "Passat", "Tiguan", "T-Roc"},
		"Mazda":      {"3", "6", "CX-5", "CX-30", "2"},
		"Nissan":     {"Sentra", "Altima", "X-Trail", "Micra", "Qashqai"},
		"Chevrolet":  {"Spark", "Cruze", "Malibu", "Captiva", "Equinox"},
		"BMW":        {"320i", "118i", "X1", "X3", "520d"},
	}
	colors   = []string{"White", "Black", "Silver", "Blue", "Red", "Grey"}
	fuels    = []string{"gasoline", "diesel", "hybrid", "electric"}
	trans    = []string{"automatic", "manual", "cvt"}
	firstNm  = []string{"James", "Mary", "Robert", "Patricia", "John", "Jennifer", "Michael", "Linda", "David", "Barbara", "Omar", "Fatima", "Hiroshi", "Priya", "Lucas"}
	lastNm   = []string{"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller", "Davis", "Rodriguez", "Martinez", "Khan", "Nakamura", "Patel", "Silva"}
	cities   = []string{"New York", "Dallas", "Chicago", "Austin", "Denver", "Miami", "Seattle", "Boston", "Phoenix", "Portland"}
	mKinds   = []string{"service", "tires", "brakes", "oil_change", "inspection", "detail"}
	vendors  = []string{"QuickLube", "AutoCare+", "FleetWerks", "TyrePro", "MechX", "DealerService"}
	plates   = []rune("ABCDEFGHJKLMNPQRSTUVWXYZ0123456789")
)

func init() { rand.Seed(time.Now().UnixNano()) }

func plate() string {
	b := make([]rune, 7)
	for i := range b {
		b[i] = plates[rand.Intn(len(plates))]
	}
	return string(b)
}
func vin() string {
	const alph = "ABCDEFGHJKLMNPRSTUVWXYZ0123456789"
	b := make([]byte, 17)
	for i := range b {
		b[i] = alph[rand.Intn(len(alph))]
	}
	return string(b)
}
func pick[T any](s []T) T { return s[rand.Intn(len(s))] }

func main() {
	_ = godotenv.Load()
	slug := flag.String("company", "", "company slug to seed into")
	nVehicles := flag.Int("vehicles", 10, "")
	nCustomers := flag.Int("customers", 10, "")
	nBookings := flag.Int("bookings", 10, "")
	nMaint := flag.Int("maintenance", 10, "")
	flag.Parse()
	if *slug == "" {
		log.Fatal("missing -company <slug>")
	}
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://postgres:postgres@localhost:5432/rental?sslmode=disable"
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	var companyID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM companies WHERE slug=$1`, *slug).Scan(&companyID); err != nil {
		log.Fatalf("company %q not found: %v", *slug, err)
	}

	// Make sure one branch exists.
	var branchID uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO branches (company_id, name, city, country, timezone)
		SELECT $1, 'Main Branch', 'HQ', 'US', 'UTC'
		WHERE NOT EXISTS (SELECT 1 FROM branches WHERE company_id=$1)
		RETURNING id
	`, companyID).Scan(&branchID)
	if err != nil {
		_ = pool.QueryRow(ctx, `SELECT id FROM branches WHERE company_id=$1 LIMIT 1`, companyID).Scan(&branchID)
	}
	fmt.Printf("Seeding into company %s (%s)\n", *slug, companyID)
	fmt.Printf("Branch: %s\n", branchID)

	// ---- Vehicles ----
	vehicleIDs := make([]uuid.UUID, 0, *nVehicles)
	for i := 0; i < *nVehicles; i++ {
		mk := pick(makes)
		md := pick(models[mk])
		yr := 2015 + rand.Intn(10)
		dr := 2500 + rand.Intn(9000)   // $25–$120/day
		dp := 15000 + rand.Intn(50000) // $150–$650 deposit
		odo := 10000 + rand.Intn(90000)
		fn := fmt.Sprintf("FL-%04d", 1000+rand.Intn(9000))
		pl := plate()
		var vid uuid.UUID
		err := pool.QueryRow(ctx, `
			INSERT INTO vehicles
			  (company_id, branch_id, fleet_number, license_plate, vin, make, model, year, trim,
			   transmission, fuel_type, seats, color_exterior, odometer_km, daily_rate_cents, deposit_cents, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'base',$9,$10,$11,$12,$13,$14,$15,'available')
			RETURNING id
		`, companyID, branchID, fn, pl, vin(), mk, md, yr,
			pick(trans), pick(fuels), 4+rand.Intn(4), pick(colors), odo, dr, dp).Scan(&vid)
		if err != nil {
			log.Printf("vehicle %d skipped: %v", i, err)
			continue
		}
		vehicleIDs = append(vehicleIDs, vid)
	}
	fmt.Printf("Inserted %d vehicles\n", len(vehicleIDs))

	// ---- Customers ----
	customerIDs := make([]uuid.UUID, 0, *nCustomers)
	for i := 0; i < *nCustomers; i++ {
		fn := pick(firstNm)
		ln := pick(lastNm)
		em := fmt.Sprintf("%s.%s%d@example.com", fn, ln, rand.Intn(999))
		ph := fmt.Sprintf("+1-%03d-%04d", 100+rand.Intn(900), 1000+rand.Intn(9000))
		exp := time.Now().AddDate(2+rand.Intn(5), 0, 0).Format("2006-01-02")
		var cid uuid.UUID
		err := pool.QueryRow(ctx, `
			INSERT INTO customers (company_id, first_name, last_name, email, phone, city, country, license_number, license_expiry)
			VALUES ($1,$2,$3,LOWER($4),$5,$6,'US',$7,$8::date)
			RETURNING id
		`, companyID, fn, ln, em, ph, pick(cities),
			fmt.Sprintf("DL-%06d", rand.Intn(999999)), exp).Scan(&cid)
		if err != nil {
			log.Printf("customer %d skipped: %v", i, err)
			continue
		}
		customerIDs = append(customerIDs, cid)
	}
	fmt.Printf("Inserted %d customers\n", len(customerIDs))

	// ---- Bookings ----
	// Spread them over the next 60 days using NON-OVERLAPPING windows per vehicle (so the
	// exclusion constraint doesn't bite).
	nextStart := map[uuid.UUID]time.Time{}
	for _, v := range vehicleIDs {
		nextStart[v] = time.Now().Add(time.Duration(rand.Intn(72)) * time.Hour).Truncate(time.Hour)
	}
	statuses := []string{"pending", "confirmed", "checked_out", "returned", "cancelled"}
	inserted := 0
	for i := 0; i < *nBookings && len(vehicleIDs) > 0 && len(customerIDs) > 0; i++ {
		vid := vehicleIDs[i%len(vehicleIDs)]
		cid := pick(customerIDs)
		start := nextStart[vid]
		days := 1 + rand.Intn(7)
		end := start.Add(time.Duration(days*24) * time.Hour)
		nextStart[vid] = end.Add(2 * time.Hour) // leave a small gap

		var daily int
		_ = pool.QueryRow(ctx, `SELECT daily_rate_cents FROM vehicles WHERE id=$1`, vid).Scan(&daily)
		subtotal := daily * days
		tax := subtotal / 10
		total := subtotal + tax
		paid := 0
		status := pick(statuses)
		if status == "confirmed" || status == "checked_out" || status == "returned" {
			paid = total
		}

		var bn string
		err := pool.QueryRow(ctx, `
			INSERT INTO reservations
			  (company_id, customer_id, vehicle_id, pickup_branch_id, return_branch_id,
			   pickup_at, return_at, status, daily_rate_cents, rental_days,
			   subtotal_cents, tax_cents, total_cents, paid_cents, source)
			VALUES ($1,$2,$3,$4,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'manager')
			RETURNING booking_number
		`, companyID, cid, vid, branchID, start, end, status, daily, days,
			subtotal, tax, total, paid).Scan(&bn)
		if err != nil {
			log.Printf("booking %d skipped: %v", i, err)
			continue
		}
		inserted++
	}
	fmt.Printf("Inserted %d bookings\n", inserted)

	// ---- Maintenance ----
	m := 0
	for i := 0; i < *nMaint && len(vehicleIDs) > 0; i++ {
		vid := pick(vehicleIDs)
		kind := pick(mKinds)
		cost := 2000 + rand.Intn(80000)
		odo := 10000 + rand.Intn(100000)
		performed := time.Now().AddDate(0, 0, -rand.Intn(120)).Format("2006-01-02")
		_, err := pool.Exec(ctx, `
			INSERT INTO maintenance_records (company_id, vehicle_id, kind, description, cost_cents, odometer_km, vendor, performed_on)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8::date)
		`, companyID, vid, kind, "routine "+kind, cost, odo, pick(vendors), performed)
		if err != nil {
			log.Printf("maint %d skipped: %v", i, err)
			continue
		}
		m++
	}
	fmt.Printf("Inserted %d maintenance records\n", m)

	fmt.Println("Done.")
}
