package persistence

// scanner and rowsScanner are minimal interfaces so the scan helpers accept both
// pgx.Row and pgx.Rows without importing the driver here.
type scanner interface {
	Scan(dest ...any) error
}

type rowsScanner interface {
	Scan(dest ...any) error
	Close()
	Next() bool
}
