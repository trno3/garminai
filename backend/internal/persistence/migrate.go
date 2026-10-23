package persistence

import (
	"context"
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies every *.sql file in the embedded migrations directory in
// lexical order. It is intentionally simple: one schema, applied once.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	names, err := migrations.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var files []string
	for _, n := range names {
		if !n.IsDir() && strings.HasSuffix(n.Name(), ".sql") {
			files = append(files, n.Name())
		}
	}
	sort.Strings(files)

	for _, name := range files {
		data, err := migrations.ReadFile(path.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := pool.Exec(ctx, string(data)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}
	return nil
}
