package config

import (
	"path/filepath"
	"testing"
)

func TestMachineStateDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")

	db, err := OpenMachineStateDBAtPath(path)
	if err != nil {
		t.Fatalf("failed to open machine state database: %v", err)
	}

	// absent keys read as absent
	_, ok, err := db.GetFloat("sidePanelWidth")
	if err != nil {
		t.Fatalf("failed to read absent key: %v", err)
	}
	if ok {
		t.Errorf("expected absent key to read as absent")
	}

	// setting a value persists it
	if err := db.SetFloat("sidePanelWidth", 0.45); err != nil {
		t.Fatalf("failed to set value: %v", err)
	}
	value, ok, err := db.GetFloat("sidePanelWidth")
	if err != nil || !ok {
		t.Fatalf("expected set value to be present, got ok=%v err=%v", ok, err)
	}
	if value != 0.45 {
		t.Errorf("expected 0.45, got %v", value)
	}

	// setting it again overwrites
	if err := db.SetFloat("sidePanelWidth", 0.65); err != nil {
		t.Fatalf("failed to overwrite value: %v", err)
	}
	value, _, _ = db.GetFloat("sidePanelWidth")
	if value != 0.65 {
		t.Errorf("expected 0.65 after overwrite, got %v", value)
	}

	// deleting removes the value
	if err := db.Delete("sidePanelWidth"); err != nil {
		t.Fatalf("failed to delete value: %v", err)
	}
	_, ok, err = db.GetFloat("sidePanelWidth")
	if err != nil || ok {
		t.Errorf("expected deleted value to be absent, got ok=%v err=%v", ok, err)
	}

	// unparseable values read as absent rather than erroring
	if err := db.SetFloat("sidePanelWidth", 0.3); err != nil {
		t.Fatalf("failed to set value: %v", err)
	}
	if _, err := db.db.Exec("UPDATE settings SET value = 'garbage' WHERE key = 'sidePanelWidth'"); err != nil {
		t.Fatalf("failed to corrupt value: %v", err)
	}
	_, ok, err = db.GetFloat("sidePanelWidth")
	if err != nil {
		t.Fatalf("corrupt value should not error: %v", err)
	}
	if ok {
		t.Errorf("expected corrupt value to read as absent")
	}

	// set it once more: this is the value that should survive reopening
	if err := db.SetFloat("sidePanelWidth", 0.3); err != nil {
		t.Fatalf("failed to set value: %v", err)
	}

	if err := db.Close(); err != nil {
		t.Fatalf("failed to close database: %v", err)
	}

	// values survive closing and reopening the database
	db, err = OpenMachineStateDBAtPath(path)
	if err != nil {
		t.Fatalf("failed to reopen machine state database: %v", err)
	}
	defer db.Close()

	value, ok, err = db.GetFloat("sidePanelWidth")
	if err != nil || !ok {
		t.Fatalf("expected value to survive reopening, got ok=%v err=%v", ok, err)
	}
	if value != 0.3 {
		t.Errorf("expected 0.3 after reopening, got %v", value)
	}
}
