package db

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type User struct {
	UserID      int64
	Username    string
	AccessToken string
	ReqID       string
	Proxy       string
	MinAmount   float64
	MaxAmount   float64
	IsRunning   bool
}

type DB struct {
	conn *sql.DB
}

type DashboardTotals struct {
	TotalUsers     int
	UsersWithToken int
	RunningUsers   int
	OrdersTotal    int // sniped + missed (excludes penalized)
	Volume24h      float64
	VolumeAllTime  float64
	OrdersSniped   int // successful captures
	OrdersMissed   int // failed attempts (Invalid status)
}

type DashboardUserStat struct {
	UserID        int64
	Username      string
	HasToken      bool
	IsRunning     bool
	MinAmount     float64
	MaxAmount     float64
	OrdersTotal   int
	SnipedOrders  int
	MissedOrders  int
	Volume24h     float64
	VolumeAllTime float64
	LastOrderAt   string
}

type RecentOrder struct {
	OrderID   string
	UserID    int64
	Username  string
	Amount    float64
	Status    string
	CreatedAt string
}

type HourlyBucket struct {
	Hour   string
	Count  int
	Volume float64
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err = conn.Exec(`PRAGMA journal_mode=WAL;`); err != nil {
		return nil, err
	}
	if _, err = conn.Exec(`PRAGMA synchronous=NORMAL;`); err != nil {
		return nil, err
	}
	d := &DB{conn: conn}
	return d, d.createTables()
}

func (d *DB) Close() error { return d.conn.Close() }

func (d *DB) createTables() error {
	_, err := d.conn.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			user_id INTEGER PRIMARY KEY,
			username TEXT,
			access_token TEXT,
			req_id TEXT,
			proxy TEXT,
			min_amount REAL DEFAULT 500,
			max_amount REAL DEFAULT 5000,
			is_running INTEGER DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS orders (
			order_id TEXT PRIMARY KEY,
			user_id INTEGER,
			amount REAL,
			status TEXT,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
	`)
	return err
}

func (d *DB) AddUser(userID int64, username string) error {
	_, err := d.conn.Exec(`INSERT OR IGNORE INTO users (user_id, username) VALUES (?, ?)`, userID, username)
	return err
}

func (d *DB) UpdateToken(userID int64, token, reqID, proxy string) error {
	res, err := d.conn.Exec(`
		UPDATE users SET access_token = ?, req_id = ?, proxy = ?
		WHERE user_id = ?
	`, token, reqID, proxy, userID)
	if err != nil {
		return err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		_, err = d.conn.Exec(`
			INSERT INTO users (user_id, username, access_token, req_id, proxy)
			VALUES (?, 'Trader', ?, ?, ?)
		`, userID, token, reqID, proxy)
	}
	return err
}

func (d *DB) UpdateLimits(userID int64, minAmount, maxAmount float64) error {
	_, err := d.conn.Exec(`UPDATE users SET min_amount = ?, max_amount = ? WHERE user_id = ?`, minAmount, maxAmount, userID)
	return err
}

// ClearAccessToken removes stored credentials and marks the user as not running.
// Use when the user wants to fully disconnect (e.g. sniper still active after /stopsniper or DB out of sync).
func (d *DB) ClearAccessToken(userID int64) error {
	_, err := d.conn.Exec(`
		UPDATE users SET access_token = '', req_id = '', proxy = '', is_running = 0
		WHERE user_id = ?
	`, userID)
	return err
}

// ClearToken removes session credentials and marks the user as not running.
// Use when the user wants to fully disconnect or fix a stuck is_running flag.
func (d *DB) ClearToken(userID int64) error {
	_, err := d.conn.Exec(`
		UPDATE users SET access_token = '', req_id = '', is_running = 0
		WHERE user_id = ?
	`, userID)
	return err
}

func (d *DB) GetUser(userID int64) (User, bool, error) {
	row := d.conn.QueryRow(`
		SELECT user_id, COALESCE(username,''), COALESCE(access_token,''), COALESCE(req_id,''), COALESCE(proxy,''), min_amount, max_amount, is_running
		FROM users WHERE user_id = ?
	`, userID)
	var u User
	var running int
	err := row.Scan(&u.UserID, &u.Username, &u.AccessToken, &u.ReqID, &u.Proxy, &u.MinAmount, &u.MaxAmount, &running)
	if err == sql.ErrNoRows {
		return User{}, false, nil
	}
	if err != nil {
		return User{}, false, err
	}
	u.IsRunning = running == 1
	return u, true, nil
}

func (d *DB) GetAllUsers() ([]User, error) {
	rows, err := d.conn.Query(`
		SELECT user_id, COALESCE(username,''), COALESCE(access_token,''), COALESCE(req_id,''), COALESCE(proxy,''), min_amount, max_amount, is_running FROM users
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		var running int
		if err := rows.Scan(&u.UserID, &u.Username, &u.AccessToken, &u.ReqID, &u.Proxy, &u.MinAmount, &u.MaxAmount, &running); err != nil {
			return nil, err
		}
		u.IsRunning = running == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) GetActiveRunners() ([]User, error) {
	rows, err := d.conn.Query(`
		SELECT user_id, COALESCE(username,''), COALESCE(access_token,''), COALESCE(req_id,''), COALESCE(proxy,''), min_amount, max_amount, is_running
		FROM users WHERE is_running = 1
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		var running int
		if err := rows.Scan(&u.UserID, &u.Username, &u.AccessToken, &u.ReqID, &u.Proxy, &u.MinAmount, &u.MaxAmount, &running); err != nil {
			return nil, err
		}
		u.IsRunning = running == 1
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) SetRunningStatus(userID int64, status bool) error {
	intStatus := 0
	if status {
		intStatus = 1
	}
	_, err := d.conn.Exec(`UPDATE users SET is_running = ? WHERE user_id = ?`, intStatus, userID)
	return err
}

func (d *DB) LogOrder(orderID string, userID int64, amount float64, status string) error {
	_, err := d.conn.Exec(`
		INSERT INTO orders (order_id, user_id, amount, status)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(order_id) DO UPDATE SET status=excluded.status
	`, orderID, userID, amount, status)
	return err
}

func (d *DB) GetTotalCaughtVolume(userID int64) (float64, error) {
	row := d.conn.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM orders WHERE user_id = ? AND status = 'sniped'`, userID)
	var v float64
	return v, row.Scan(&v)
}

func (d *DB) GetDailyVolume(userID int64) (float64, error) {
	from := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	row := d.conn.QueryRow(`
		SELECT COALESCE(SUM(amount), 0)
		FROM orders
		WHERE user_id = ? AND status = 'sniped' AND created_at >= ?
	`, userID, from)
	var v float64
	return v, row.Scan(&v)
}

func (d *DB) GetDashboardTotals() (DashboardTotals, error) {
	var t DashboardTotals
	from := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	err := d.conn.QueryRow(`
		SELECT
			(SELECT COUNT(*) FROM users),
			(SELECT COUNT(*) FROM users WHERE access_token <> ''),
			(SELECT COUNT(*) FROM users WHERE is_running = 1),
			(SELECT COUNT(*) FROM orders),
			(SELECT COALESCE(SUM(amount), 0) FROM orders WHERE status = 'sniped' AND created_at >= ?),
			(SELECT COALESCE(SUM(amount), 0) FROM orders WHERE status = 'sniped'),
			(SELECT COUNT(*) FROM orders WHERE status = 'sniped'),
			(SELECT COUNT(*) FROM orders WHERE status = 'missed')
	`, from).Scan(
		&t.TotalUsers,
		&t.UsersWithToken,
		&t.RunningUsers,
		&t.OrdersTotal,
		&t.Volume24h,
		&t.VolumeAllTime,
		&t.OrdersSniped,
		&t.OrdersMissed,
	)
	return t, err
}

func (d *DB) GetDashboardUserStats(limit int) ([]DashboardUserStat, error) {
	if limit < 1 {
		limit = 200
	}
	from := time.Now().Add(-24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	rows, err := d.conn.Query(`
		SELECT
			u.user_id,
			COALESCE(u.username, ''),
			CASE WHEN u.access_token <> '' THEN 1 ELSE 0 END,
			u.is_running,
			u.min_amount,
			u.max_amount,
			COUNT(o.order_id) AS orders_total,
			SUM(CASE WHEN o.status = 'sniped' THEN 1 ELSE 0 END) AS sniped_orders,
			SUM(CASE WHEN o.status = 'missed' THEN 1 ELSE 0 END) AS missed_orders,
			COALESCE(SUM(CASE WHEN o.status = 'sniped' AND o.created_at >= ? THEN o.amount ELSE 0 END), 0) AS volume_24h,
			COALESCE(SUM(CASE WHEN o.status = 'sniped' THEN o.amount ELSE 0 END), 0) AS volume_all_time,
			COALESCE(MAX(o.created_at), '') AS last_order_at
		FROM users u
		LEFT JOIN orders o ON o.user_id = u.user_id
		GROUP BY u.user_id, u.username, u.access_token, u.is_running, u.min_amount, u.max_amount
		ORDER BY u.is_running DESC, volume_24h DESC, volume_all_time DESC, u.user_id DESC
		LIMIT ?
	`, from, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]DashboardUserStat, 0, limit)
	for rows.Next() {
		var s DashboardUserStat
		var hasToken, running int
		if err := rows.Scan(
			&s.UserID,
			&s.Username,
			&hasToken,
			&running,
			&s.MinAmount,
			&s.MaxAmount,
			&s.OrdersTotal,
			&s.SnipedOrders,
			&s.MissedOrders,
			&s.Volume24h,
			&s.VolumeAllTime,
			&s.LastOrderAt,
		); err != nil {
			return nil, err
		}
		s.HasToken = hasToken == 1
		s.IsRunning = running == 1
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) GetRecentOrders(limit int) ([]RecentOrder, error) {
	if limit < 1 {
		limit = 20
	}
	rows, err := d.conn.Query(`
		SELECT
			o.order_id,
			o.user_id,
			COALESCE(u.username, ''),
			o.amount,
			COALESCE(o.status, ''),
			COALESCE(o.created_at, '')
		FROM orders o
		LEFT JOIN users u ON u.user_id = o.user_id
		ORDER BY o.created_at DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]RecentOrder, 0, limit)
	for rows.Next() {
		var r RecentOrder
		if err := rows.Scan(&r.OrderID, &r.UserID, &r.Username, &r.Amount, &r.Status, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetHourlyActivity returns 24 buckets (oldest → newest) covering the last 24 hours.
// Empty hours are included with zero values so the chart always has a full axis.
func (d *DB) GetHourlyActivity() ([]HourlyBucket, error) {
	from := time.Now().Add(-24 * time.Hour).UTC()
	fromStr := from.Format("2006-01-02 15:04:05")

	rows, err := d.conn.Query(`
		SELECT strftime('%Y-%m-%d %H', created_at) AS hr,
		       COUNT(*), COALESCE(SUM(amount), 0)
		FROM orders
		WHERE status = 'sniped' AND created_at >= ?
		GROUP BY hr
	`, fromStr)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	raw := make(map[string]HourlyBucket, 24)
	for rows.Next() {
		var b HourlyBucket
		if err := rows.Scan(&b.Hour, &b.Count, &b.Volume); err != nil {
			return nil, err
		}
		raw[b.Hour] = b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]HourlyBucket, 0, 24)
	start := from.Truncate(time.Hour)
	for i := 0; i < 24; i++ {
		t := start.Add(time.Duration(i) * time.Hour)
		key := t.Format("2006-01-02 15")
		bucket := raw[key]
		bucket.Hour = t.Format("15:00")
		out = append(out, bucket)
	}
	return out, nil
}
