package handlers

import (
	"net/http"
	"time"

	"github.com/google/uuid"
)

// ActiveRentals: every booking the customer still has out (checked_out or confirmed-for-pickup today),
// with a flag for overdue returns. Powers the "who's got what" dashboard page.
func (d *Deps) ActiveRentals(w http.ResponseWriter, r *http.Request) {
	c := claimsFromReq(r)
	rows, err := d.DB.Pool.Query(r.Context(), `
		SELECT r.id, r.booking_number, r.status, r.pickup_at, r.return_at,
		       r.total_cents, r.paid_cents, r.notes,
		       cu.id, cu.first_name, cu.last_name, cu.email, cu.phone, cu.license_number, cu.city,
		       v.id, COALESCE(v.year::text,'')||' '||COALESCE(v.make,'')||' '||COALESCE(v.model,''),
		       v.license_plate, v.odometer_km, v.color_exterior,
		       b.name
		FROM reservations r
		LEFT JOIN customers cu ON cu.id=r.customer_id
		LEFT JOIN vehicles  v  ON v.id =r.vehicle_id
		LEFT JOIN branches  b  ON b.id =r.pickup_branch_id
		WHERE r.company_id=$1
		  AND r.status IN ('confirmed','awaiting_pickup','checked_out','return_overdue')
		ORDER BY
		  (CASE WHEN r.status='checked_out' AND r.return_at < NOW() THEN 0 ELSE 1 END),
		  r.pickup_at ASC
	`, c.CompanyID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error: "+err.Error())
		return
	}
	defer rows.Close()

	type row struct {
		ID             uuid.UUID  `json:"id"`
		BookingNumber  string     `json:"bookingNumber"`
		Status         string     `json:"status"`
		PickupAt       time.Time  `json:"pickupAt"`
		ReturnAt       time.Time  `json:"returnAt"`
		TotalCents     int        `json:"totalCents"`
		PaidCents      int        `json:"paidCents"`
		Notes          *string    `json:"notes"`
		Overdue        bool       `json:"overdue"`
		CustomerID     *uuid.UUID `json:"customerId"`
		CustomerFirst  *string    `json:"customerFirst"`
		CustomerLast   *string    `json:"customerLast"`
		CustomerEmail  *string    `json:"customerEmail"`
		CustomerPhone  *string    `json:"customerPhone"`
		CustomerLic    *string    `json:"customerLicense"`
		CustomerCity   *string    `json:"customerCity"`
		VehicleID      *uuid.UUID `json:"vehicleId"`
		VehicleLabel   string     `json:"vehicle"`
		Plate          *string    `json:"licensePlate"`
		Odometer       *int       `json:"odometerKm"`
		Color          *string    `json:"vehicleColor"`
		Branch         *string    `json:"branch"`
	}

	out := []row{}
	now := time.Now()
	for rows.Next() {
		var rr row
		if err := rows.Scan(
			&rr.ID, &rr.BookingNumber, &rr.Status, &rr.PickupAt, &rr.ReturnAt,
			&rr.TotalCents, &rr.PaidCents, &rr.Notes,
			&rr.CustomerID, &rr.CustomerFirst, &rr.CustomerLast, &rr.CustomerEmail, &rr.CustomerPhone, &rr.CustomerLic, &rr.CustomerCity,
			&rr.VehicleID, &rr.VehicleLabel, &rr.Plate, &rr.Odometer, &rr.Color,
			&rr.Branch,
		); err != nil {
			continue
		}
		rr.Overdue = rr.Status == "checked_out" && rr.ReturnAt.Before(now)
		out = append(out, rr)
	}
	writeJSON(w, http.StatusOK, out)
}
