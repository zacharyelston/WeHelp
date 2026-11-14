package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/zacharyelston/wehelp/internal/audit"
	"github.com/zacharyelston/wehelp/internal/site"
	"github.com/zacharyelston/wehelp/internal/tunnel"
)

// Supply store (#97).
//
//   - Staff keep a small catalogue on the office LAN. The public site shows
//     it and says "order in the app"; it takes no orders.
//   - Patients order in the app through the pinned tunnel: quantities of
//     active products, an optional note. Payment is at pickup or by
//     invoice, so there is no payment step.
//   - Staff see orders on the LAN and move them placed → ready → fulfilled,
//     or cancel them. A patient may cancel while an order is still placed.
//   - Orders are patient data: every change is audited in its transaction,
//     and the patient gets the generic "order_update" push on staff changes.

const (
	maxOrderLines   = 20
	maxLineQuantity = 20
	maxOpenOrders   = 5
)

type productView struct {
	ID          uuid.UUID `json:"id"`
	SKU         string    `json:"sku"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Unit        string    `json:"unit"`
	PriceCents  int       `json:"price_cents"`
	Active      bool      `json:"active"`
	Sort        int       `json:"sort"`
}

const productColumns = `id, sku, name, description, category, unit, price_cents, active, sort`

func scanProduct(row pgx.Row) (productView, error) {
	var p productView
	err := row.Scan(&p.ID, &p.SKU, &p.Name, &p.Description, &p.Category, &p.Unit, &p.PriceCents, &p.Active, &p.Sort)
	return p, err
}

// isStaffOnLAN reports a staff caller on the office LAN.
func isStaffOnLAN(r *http.Request, id Identity) bool {
	return (id.Role == "provider" || id.Role == "admin") && !tunnel.ViaRelay(r.Context())
}

// ListProducts returns the catalogue: active products for patients and
// phones, every product for staff on the LAN.
// Route: GET /api/v1/store/products
func (s *Server) ListProducts(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	q := `select ` + productColumns + ` from products where tenant_id = $1`
	if !isStaffOnLAN(r, id) {
		q += ` and active`
	}
	rows, err := s.pool.Query(r.Context(), q+` order by category, sort, name`, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (productView, error) { return scanProduct(row) })
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "scan failed")
		return
	}
	if list == nil {
		list = []productView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"products": list, "payment": "Pay at pickup or by invoice."})
}

type productBody struct {
	SKU         string `json:"sku"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	Unit        string `json:"unit"`
	PriceCents  int    `json:"price_cents"`
	Active      *bool  `json:"active"`
	Sort        int    `json:"sort"`
}

func (b *productBody) validate() error {
	b.SKU, b.Name = strings.TrimSpace(b.SKU), strings.TrimSpace(b.Name)
	b.Description, b.Category, b.Unit = strings.TrimSpace(b.Description), strings.TrimSpace(b.Category), strings.TrimSpace(b.Unit)
	switch {
	case b.SKU == "" || len(b.SKU) > 40:
		return errors.New("sku is required (at most 40 characters)")
	case b.Name == "" || len(b.Name) > 120:
		return errors.New("name is required (at most 120 characters)")
	case len(b.Description) > 1000 || len(b.Category) > 60 || len(b.Unit) > 60:
		return errors.New("description, category or unit is too long")
	case b.PriceCents < 0 || b.PriceCents > 1000000:
		return errors.New("price_cents must be 0 to 1000000")
	}
	return nil
}

// CreateProduct adds a catalogue item. Staff, LAN only.
// Route: POST /api/v1/store/products
func (s *Server) CreateProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	var b productBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := b.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	active := b.Active == nil || *b.Active
	p, err := scanProduct(s.pool.QueryRow(r.Context(), `
		insert into products (tenant_id, sku, name, description, category, unit, price_cents, active, sort)
		values ($1, $2, $3, $4, $5, $6, $7, $8, $9) returning `+productColumns,
		id.TenantID, b.SKU, b.Name, b.Description, b.Category, b.Unit, b.PriceCents, active, b.Sort))
	if productConflict(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func productConflict(w http.ResponseWriter, err error) bool {
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return false
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		writeErr(w, http.StatusConflict, "another product has that sku")
	case errors.Is(err, pgx.ErrNoRows):
		writeErr(w, http.StatusNotFound, "product not found")
	default:
		writeErr(w, http.StatusInternalServerError, "save failed")
	}
	return true
}

// UpdateProduct replaces a catalogue item's fields. Staff, LAN only.
// Placed orders keep the name and price they were placed with.
// Route: PUT /api/v1/store/products/{productID}
func (s *Server) UpdateProduct(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	pid, err := uuid.Parse(chi.URLParam(r, "productID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid product id")
		return
	}
	var b productBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := b.validate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	active := b.Active == nil || *b.Active
	p, err := scanProduct(s.pool.QueryRow(r.Context(), `
		update products set sku = $3, name = $4, description = $5, category = $6, unit = $7, price_cents = $8,
			active = $9, sort = $10, updated_at = now()
		where id = $1 and tenant_id = $2 returning `+productColumns,
		pid, id.TenantID, b.SKU, b.Name, b.Description, b.Category, b.Unit, b.PriceCents, active, b.Sort))
	if productConflict(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// AddDemoProducts loads the ten demo products, skipping SKUs already
// there. Staff, LAN only.
// Route: POST /api/v1/store/products/demo
func (s *Server) AddDemoProducts(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	added, err := site.AddDemoCatalogue(r.Context(), s.pool, id.TenantID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "load failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"added": added})
}

// Orders

type orderItemView struct {
	ProductID      uuid.UUID `json:"product_id"`
	Name           string    `json:"name"`
	Unit           string    `json:"unit"`
	UnitPriceCents int       `json:"unit_price_cents"`
	Quantity       int       `json:"quantity"`
}

type orderView struct {
	ID          uuid.UUID       `json:"id"`
	PatientID   uuid.UUID       `json:"patient_id"`
	PatientName string          `json:"patient_name,omitempty"` // staff view only
	Status      string          `json:"status"`
	Note        string          `json:"note"`
	StaffNote   string          `json:"staff_note"`
	TotalCents  int             `json:"total_cents"`
	Items       []orderItemView `json:"items"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	ClosedAt    *time.Time      `json:"closed_at"`
}

const orderColumns = `o.id, o.patient_id, o.status, o.note, o.staff_note, o.total_cents, o.created_at, o.updated_at, o.closed_at`

func scanOrder(row pgx.Row) (orderView, error) {
	var o orderView
	err := row.Scan(&o.ID, &o.PatientID, &o.Status, &o.Note, &o.StaffNote, &o.TotalCents, &o.CreatedAt, &o.UpdatedAt, &o.ClosedAt)
	return o, err
}

// withItems fills in each order's lines with one query.
func withItems(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, tenant uuid.UUID, orders []orderView) error {
	if len(orders) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(orders))
	at := map[uuid.UUID]int{}
	for i, o := range orders {
		ids[i], at[o.ID] = o.ID, i
		orders[i].Items = []orderItemView{}
	}
	rows, err := q.Query(ctx, `select order_id, product_id, name, unit, unit_price_cents, quantity from order_items
		where tenant_id = $1 and order_id = any($2) order by name`, tenant, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var oid uuid.UUID
		var it orderItemView
		if err := rows.Scan(&oid, &it.ProductID, &it.Name, &it.Unit, &it.UnitPriceCents, &it.Quantity); err != nil {
			return err
		}
		orders[at[oid]].Items = append(orders[at[oid]].Items, it)
	}
	return rows.Err()
}

type orderLine struct {
	ProductID uuid.UUID `json:"product_id"`
	Quantity  int       `json:"quantity"`
}

type createOrderBody struct {
	Items []orderLine `json:"items"`
	Note  string      `json:"note"`
}

// CreateOrder places a supply order for the calling patient.
// Route: POST /api/v1/orders
func (s *Server) CreateOrder(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.Role != "patient" {
		writeErr(w, http.StatusForbidden, "only patients place orders")
		return
	}
	var b createOrderBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	b.Note = strings.TrimSpace(b.Note)
	if len(b.Note) > 500 {
		writeErr(w, http.StatusBadRequest, "note is limited to 500 characters")
		return
	}
	if len(b.Items) == 0 || len(b.Items) > maxOrderLines {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("order 1 to %d different products", maxOrderLines))
		return
	}
	qty := map[uuid.UUID]int{}
	ids := []uuid.UUID{}
	for _, l := range b.Items {
		if l.Quantity < 1 || l.Quantity > maxLineQuantity {
			writeErr(w, http.StatusBadRequest, fmt.Sprintf("each quantity must be 1 to %d", maxLineQuantity))
			return
		}
		if _, dup := qty[l.ProductID]; dup {
			writeErr(w, http.StatusBadRequest, "list each product once")
			return
		}
		qty[l.ProductID] = l.Quantity
		ids = append(ids, l.ProductID)
	}

	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	// Count open orders under a per-patient lock so two at once can't both
	// slip under the limit.
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('orders:' || $1::text, 1))`, id.UserID); err != nil {
		writeErr(w, http.StatusInternalServerError, "lock failed")
		return
	}
	var open int
	if err := tx.QueryRow(ctx, `select count(*) from orders where tenant_id = $1 and patient_id = $2 and status in ('placed', 'ready')`,
		id.TenantID, id.UserID).Scan(&open); err != nil {
		writeErr(w, http.StatusInternalServerError, "count failed")
		return
	}
	if open >= maxOpenOrders {
		writeErr(w, http.StatusConflict, fmt.Sprintf("you have %d orders in progress; we'll be in touch about those first", open))
		return
	}
	rows, err := tx.Query(ctx, `select id, name, unit, price_cents from products
		where tenant_id = $1 and active and id = any($2)`, id.TenantID, ids)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (orderItemView, error) {
		var it orderItemView
		err := row.Scan(&it.ProductID, &it.Name, &it.Unit, &it.UnitPriceCents)
		it.Quantity = qty[it.ProductID]
		return it, err
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	if len(items) != len(ids) {
		writeErr(w, http.StatusBadRequest, "a product in the order isn't available")
		return
	}
	total := 0
	for _, it := range items {
		total += it.UnitPriceCents * it.Quantity
	}
	o, err := scanOrder(tx.QueryRow(ctx, `insert into orders as o (tenant_id, patient_id, note, total_cents)
		values ($1, $2, $3, $4) returning `+orderColumns, id.TenantID, id.UserID, b.Note, total))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "insert failed")
		return
	}
	for _, it := range items {
		if _, err := tx.Exec(ctx, `insert into order_items (tenant_id, order_id, product_id, name, unit, unit_price_cents, quantity)
			values ($1, $2, $3, $4, $5, $6, $7)`, id.TenantID, o.ID, it.ProductID, it.Name, it.Unit, it.UnitPriceCents, it.Quantity); err != nil {
			writeErr(w, http.StatusInternalServerError, "insert failed")
			return
		}
	}
	o.Items = items
	if err := auditOrder(ctx, tx, id, "order.create", o.ID, map[string]any{"lines": len(items), "total_cents": total}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

// ListOrders returns a patient's own orders, or for staff on the office LAN
// every order with the patient's name (?status= filters). Newest first, 200
// at most. Staff can't list orders through the Relay.
// Route: GET /api/v1/orders
func (s *Server) ListOrders(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	staff := id.Role == "provider" || id.Role == "admin"
	if staff && tunnel.ViaRelay(r.Context()) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	q := `select ` + orderColumns + `, u.display_name from orders o join users u on u.id = o.patient_id where o.tenant_id = $1`
	args := []any{id.TenantID}
	if !staff {
		q += ` and o.patient_id = $2`
		args = append(args, id.UserID)
	}
	if st := r.URL.Query().Get("status"); st != "" {
		args = append(args, st)
		q += fmt.Sprintf(` and o.status = $%d`, len(args))
	}
	rows, err := s.pool.Query(r.Context(), q+` order by o.created_at desc limit 200`, args...)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (orderView, error) {
		var o orderView
		err := row.Scan(&o.ID, &o.PatientID, &o.Status, &o.Note, &o.StaffNote, &o.TotalCents, &o.CreatedAt, &o.UpdatedAt, &o.ClosedAt, &o.PatientName)
		if !staff {
			o.PatientName = ""
		}
		return o, err
	})
	if err == nil {
		err = withItems(r.Context(), s.pool, id.TenantID, list)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	if list == nil {
		list = []orderView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"orders": list})
}

// CancelOrder lets a patient cancel their own order while it is placed.
// Route: POST /api/v1/orders/{orderID}/cancel
func (s *Server) CancelOrder(w http.ResponseWriter, r *http.Request) {
	id, _ := identityFrom(r.Context())
	if id.Role != "patient" {
		writeErr(w, http.StatusForbidden, "staff change orders with /status")
		return
	}
	s.changeOrder(w, r, id, func(o *orderView) (int, error) {
		if o.PatientID != id.UserID {
			return http.StatusNotFound, errors.New("order not found")
		}
		if o.Status != "placed" {
			return http.StatusConflict, errors.New("this order is already " + o.Status + "; message the practice to change it")
		}
		o.Status = "cancelled"
		return 0, nil
	}, false)
}

type orderStatusBody struct {
	Status string `json:"status"`
	Note   string `json:"note"`
}

// orderNext lists the moves staff may make from each status.
var orderNext = map[string][]string{
	"placed": {"ready", "fulfilled", "cancelled"},
	"ready":  {"fulfilled", "cancelled", "placed"},
}

// SetOrderStatus moves an order on: ready, fulfilled or cancelled, with an
// optional note the patient sees. Staff, LAN only.
// Route: POST /api/v1/orders/{orderID}/status
func (s *Server) SetOrderStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := staffOnLAN(w, r)
	if !ok {
		return
	}
	var b orderStatusBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid body")
		return
	}
	b.Note = strings.TrimSpace(b.Note)
	if len(b.Note) > 500 {
		writeErr(w, http.StatusBadRequest, "note is limited to 500 characters")
		return
	}
	s.changeOrder(w, r, id, func(o *orderView) (int, error) {
		allowed := false
		for _, next := range orderNext[o.Status] {
			allowed = allowed || next == b.Status
		}
		if !allowed {
			return http.StatusConflict, fmt.Errorf("an order that is %s can't become %q", o.Status, b.Status)
		}
		o.Status = b.Status
		if b.Note != "" {
			o.StaffNote = b.Note
		}
		return 0, nil
	}, true)
}

// changeOrder locks an order, applies change, saves and audits it in one
// transaction, then (for staff changes) pushes "order_update".
func (s *Server) changeOrder(w http.ResponseWriter, r *http.Request, id Identity, change func(o *orderView) (int, error), notify bool) {
	oid, err := uuid.Parse(chi.URLParam(r, "orderID"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid order id")
		return
	}
	ctx := r.Context()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "tx failed")
		return
	}
	defer tx.Rollback(ctx)
	o, err := scanOrder(tx.QueryRow(ctx, `select `+orderColumns+` from orders o where o.id = $1 and o.tenant_id = $2 for update`, oid, id.TenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		writeErr(w, http.StatusNotFound, "order not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	from := o.Status
	if status, err := change(&o); err != nil {
		writeErr(w, status, err.Error())
		return
	}
	o, err = scanOrder(tx.QueryRow(ctx, `
		update orders o set status = $2, staff_note = $3, updated_at = now(),
			closed_at = case when $2 in ('fulfilled', 'cancelled') then now() end
		where o.id = $1 returning `+orderColumns, o.ID, o.Status, o.StaffNote))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	one := []orderView{o}
	if err := withItems(ctx, tx, id.TenantID, one); err != nil {
		writeErr(w, http.StatusInternalServerError, "query failed")
		return
	}
	o = one[0]
	if err := auditOrder(ctx, tx, id, "order."+o.Status, o.ID, map[string]any{"from": from, "to": o.Status}); err != nil {
		writeErr(w, http.StatusInternalServerError, "audit failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		writeErr(w, http.StatusInternalServerError, "commit failed")
		return
	}
	if notify {
		s.notify(o.PatientID, "order_update")
	}
	writeJSON(w, http.StatusOK, o)
}

func auditOrder(ctx context.Context, tx pgx.Tx, id Identity, action string, orderID uuid.UUID, detail map[string]any) error {
	d, _ := json.Marshal(detail)
	return audit.Record(ctx, tx, audit.Event{
		TenantID: id.TenantID, ActorID: &id.UserID,
		Action: action, ResourceType: "order", ResourceID: orderID.String(), Detail: d,
	})
}
