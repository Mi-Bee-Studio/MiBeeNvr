package recorder

import "time"

// DB insert retry policy for segment metadata — SQLite busy is a known
// contention point; one tunable for every recorder (#876).
const (
	dbInsertRetries = 3
	dbInsertBackoff = 500 * time.Millisecond
)
