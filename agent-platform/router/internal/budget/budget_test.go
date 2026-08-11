package budget

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openTestDB points the package-level db at a temp sqlite file and runs the
// same schema creation initDB() uses, without touching the real /data path.
func openTestDB(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budget-test.db")

	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	_, err = sqlDB.Exec(`
		CREATE TABLE IF NOT EXISTS calls (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts REAL NOT NULL,
			model TEXT NOT NULL,
			input_tokens INTEGER NOT NULL,
			output_tokens INTEGER NOT NULL,
			cost_usd REAL NOT NULL,
			task_type TEXT
		)
	`)
	if err != nil {
		t.Fatalf("failed to create schema: %v", err)
	}

	db = sqlDB
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Logf("failed to close test db: %v", err)
		}
	})
}

func TestRecordCall_KnownModelPricing(t *testing.T) {
	openTestDB(t)

	cost, err := RecordCall("claude-sonnet-4-6", 1_000_000, 1_000_000, "draft_reply")
	if err != nil {
		t.Fatalf("recordCall returned error: %v", err)
	}
	want := 3.00 + 15.00
	if cost != want {
		t.Errorf("recordCall cost = %v, want %v", cost, want)
	}
}

func TestRecordCall_UnknownModelFallsBackToDefaultPricing(t *testing.T) {
	openTestDB(t)

	cost, err := RecordCall("some-future-model", 1_000_000, 1_000_000, "draft_reply")
	if err != nil {
		t.Fatalf("recordCall returned error: %v", err)
	}
	want := 3.00 + 15.00 // default fallback pricing in pricing map
	if cost != want {
		t.Errorf("recordCall fallback cost = %v, want %v", cost, want)
	}
}

func TestGetBudgetStatus_UnderAndOverBudget(t *testing.T) {
	openTestDB(t)

	status, err := GetBudgetStatus(10.0)
	if err != nil {
		t.Fatalf("getBudgetStatus returned error: %v", err)
	}
	if status.OverBudget {
		t.Errorf("expected fresh db to not be over budget")
	}
	if status.Remaining != 10.0 {
		t.Errorf("expected remaining = 10.0, got %v", status.Remaining)
	}

	// Spend enough to exceed the cap.
	if _, err := RecordCall("claude-sonnet-4-6", 4_000_000, 0, "draft_reply"); err != nil {
		t.Fatalf("recordCall returned error: %v", err)
	}

	status, err = GetBudgetStatus(10.0)
	if err != nil {
		t.Fatalf("getBudgetStatus returned error: %v", err)
	}
	if !status.OverBudget {
		t.Errorf("expected status to be over budget after spending $12, got spend=%v", status.MonthSpend)
	}
	if status.Remaining >= 0 {
		t.Errorf("expected negative remaining budget, got %v", status.Remaining)
	}
}
