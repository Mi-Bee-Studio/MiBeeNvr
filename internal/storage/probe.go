package storage

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

// probeDSN mirrors the DSN New() uses (WAL + mmap + busy_timeout) so the probe
// exercises the exact access pattern the NVR database will need on this path.
// Some platforms (fnOS external storage) accept plain file creation but reject
// the syscalls SQLite depends on — only a real SQLite round-trip catches that.
const probeDSNSuffix = "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(15000)&_pragma=temp_store(MEMORY)"

// ProbeRoot verifies that dir can host the NVR recording root: it must be
// creatable and able to run a throwaway SQLite database (create table + drop +
// WAL artifacts). It leaves no files behind on success. The error message is
// user-facing (surfaced as the 400 body of PUT /api/settings).
func ProbeRoot(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create %q: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".nvr-probe-*.db")
	if err != nil {
		return fmt.Errorf("cannot create files in %q: %w", dir, err)
	}
	probePath := f.Name()
	f.Close()
	os.Remove(probePath) // let SQLite create it fresh with WAL mode

	db, err := sql.Open("sqlite", probePath+probeDSNSuffix)
	if err != nil {
		os.Remove(probePath)
		return fmt.Errorf("sqlite open failed on %q: %w", dir, err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE nvr_probe(x); DROP TABLE nvr_probe;"); err != nil {
		os.Remove(probePath)
		os.Remove(probePath + "-wal")
		os.Remove(probePath + "-shm")
		return fmt.Errorf("sqlite cannot operate in %q: %w", dir, err)
	}
	db.Close()
	os.Remove(probePath)
	os.Remove(probePath + "-wal")
	os.Remove(probePath + "-shm")
	return nil
}
