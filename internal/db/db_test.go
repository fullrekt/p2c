package db

import (
	"os"
	"testing"
	"time"
)

func newTestDB(t *testing.T) (*DB, func()) {
	t.Helper()
	f, err := os.CreateTemp("", "test-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	d, err := Open(f.Name())
	if err != nil {
		os.Remove(f.Name())
		t.Fatal(err)
	}
	return d, func() {
		d.Close()
		os.Remove(f.Name())
	}
}

func TestAddUserAndGetUser(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	if err := d.AddUser(42, "alice"); err != nil {
		t.Fatalf("AddUser: %v", err)
	}

	u, ok, err := d.GetUser(42)
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if !ok {
		t.Fatal("expected user to exist")
	}
	if u.UserID != 42 || u.Username != "alice" {
		t.Errorf("got user %+v", u)
	}
}

func TestAddUserIdempotent(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(1, "bob")
	// second call must not fail or overwrite
	if err := d.AddUser(1, "charlie"); err != nil {
		t.Fatalf("AddUser second: %v", err)
	}
	u, _, _ := d.GetUser(1)
	if u.Username != "bob" {
		t.Errorf("INSERT OR IGNORE should have kept original username, got %q", u.Username)
	}
}

func TestGetUserNotFound(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_, ok, err := d.GetUser(999)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected user not found")
	}
}

func TestUpdateToken(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(10, "user10")
	if err := d.UpdateToken(10, "tok123", "req456", ""); err != nil {
		t.Fatalf("UpdateToken: %v", err)
	}
	u, _, _ := d.GetUser(10)
	if u.AccessToken != "tok123" || u.ReqID != "req456" {
		t.Errorf("token not updated: %+v", u)
	}
}

func TestUpdateLimits(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(20, "user20")
	if err := d.UpdateLimits(20, 100.0, 500.0); err != nil {
		t.Fatalf("UpdateLimits: %v", err)
	}
	u, _, _ := d.GetUser(20)
	if u.MinAmount != 100.0 || u.MaxAmount != 500.0 {
		t.Errorf("limits not updated: %+v", u)
	}
}

func TestSetRunningStatus(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(30, "user30")

	if err := d.SetRunningStatus(30, true); err != nil {
		t.Fatalf("SetRunningStatus true: %v", err)
	}
	u, _, _ := d.GetUser(30)
	if !u.IsRunning {
		t.Error("expected IsRunning = true")
	}

	if err := d.SetRunningStatus(30, false); err != nil {
		t.Fatalf("SetRunningStatus false: %v", err)
	}
	u, _, _ = d.GetUser(30)
	if u.IsRunning {
		t.Error("expected IsRunning = false")
	}
}

func TestGetActiveRunners(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(40, "u40")
	_ = d.AddUser(41, "u41")
	_ = d.SetRunningStatus(40, true)

	runners, err := d.GetActiveRunners()
	if err != nil {
		t.Fatalf("GetActiveRunners: %v", err)
	}
	if len(runners) != 1 || runners[0].UserID != 40 {
		t.Errorf("expected one runner (40), got %+v", runners)
	}
}

func TestLogOrderAndDailyVolume(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(50, "u50")
	if err := d.LogOrder("ord1", 50, 1000.0, "sniped"); err != nil {
		t.Fatalf("LogOrder: %v", err)
	}
	if err := d.LogOrder("ord2", 50, 500.0, "sniped"); err != nil {
		t.Fatalf("LogOrder: %v", err)
	}

	v, err := d.GetDailyVolume(50)
	if err != nil {
		t.Fatalf("GetDailyVolume: %v", err)
	}
	if v != 1500.0 {
		t.Errorf("daily volume = %.2f, want 1500.00", v)
	}
}

func TestLogOrderUpsert(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(60, "u60")
	_ = d.LogOrder("ord1", 60, 200.0, "sniped")
	// same order_id with different status = upsert
	if err := d.LogOrder("ord1", 60, 200.0, "completed"); err != nil {
		t.Fatalf("LogOrder upsert: %v", err)
	}
}

func TestClearAccessToken(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(70, "u70")
	_ = d.UpdateToken(70, "secret", "req1", "")
	_ = d.SetRunningStatus(70, true)

	if err := d.ClearAccessToken(70); err != nil {
		t.Fatalf("ClearAccessToken: %v", err)
	}
	u, _, _ := d.GetUser(70)
	if u.AccessToken != "" || u.IsRunning {
		t.Errorf("expected cleared token and stopped status, got %+v", u)
	}
}

func TestGetDashboardTotals(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(80, "u80")
	_ = d.UpdateToken(80, "tok", "req", "")
	_ = d.SetRunningStatus(80, true)
	_ = d.LogOrder("o1", 80, 300.0, "sniped")

	totals, err := d.GetDashboardTotals()
	if err != nil {
		t.Fatalf("GetDashboardTotals: %v", err)
	}
	if totals.TotalUsers != 1 {
		t.Errorf("TotalUsers = %d, want 1", totals.TotalUsers)
	}
	if totals.UsersWithToken != 1 {
		t.Errorf("UsersWithToken = %d, want 1", totals.UsersWithToken)
	}
	if totals.RunningUsers != 1 {
		t.Errorf("RunningUsers = %d, want 1", totals.RunningUsers)
	}
	if totals.OrdersTotal != 1 {
		t.Errorf("OrdersTotal = %d, want 1", totals.OrdersTotal)
	}
}

func TestGetDailyVolumeEmpty(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(90, "u90")
	v, err := d.GetDailyVolume(90)
	if err != nil {
		t.Fatalf("GetDailyVolume: %v", err)
	}
	if v != 0 {
		t.Errorf("expected 0, got %.2f", v)
	}
}

func TestGetDashboardUserStats(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(100, "u100")
	_ = d.LogOrder("o10", 100, 999.0, "sniped")

	stats, err := d.GetDashboardUserStats(10)
	if err != nil {
		t.Fatalf("GetDashboardUserStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 stat, got %d", len(stats))
	}
	if stats[0].UserID != 100 {
		t.Errorf("UserID = %d, want 100", stats[0].UserID)
	}
	if stats[0].VolumeAllTime != 999.0 {
		t.Errorf("VolumeAllTime = %.2f, want 999.00", stats[0].VolumeAllTime)
	}
}

// Verify that GetDailyVolume only counts orders within 24h window.
func TestGetDailyVolumeWindow(t *testing.T) {
	d, cleanup := newTestDB(t)
	defer cleanup()

	_ = d.AddUser(110, "u110")
	// Insert old order directly via SQL to simulate a >24h old record.
	oldTime := time.Now().Add(-25 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	_, err := d.conn.Exec(
		`INSERT INTO orders (order_id, user_id, amount, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		"old1", 110, 500.0, "sniped", oldTime,
	)
	if err != nil {
		t.Fatalf("insert old order: %v", err)
	}
	_ = d.LogOrder("new1", 110, 200.0, "sniped")

	v, err := d.GetDailyVolume(110)
	if err != nil {
		t.Fatalf("GetDailyVolume: %v", err)
	}
	if v != 200.0 {
		t.Errorf("daily volume = %.2f, want 200.00 (old order should be excluded)", v)
	}
}
