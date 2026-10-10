package cli

// Engines register themselves in init.
import (
	_ "github.com/anderson-0/qh/internal/engine/mysql"
	_ "github.com/anderson-0/qh/internal/engine/postgres"
	_ "github.com/anderson-0/qh/internal/engine/sqlite"
)
