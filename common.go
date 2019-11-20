package main

import (
	"fmt"
)

func ApplyMigrations(dir MigrationDirection, dryrun bool, limit int) error {
	env, err := GetEnvironment()
	if err != nil {
		return fmt.Errorf("Could not parse config: %s", err)
	}

	db, _, err := GetConnection(env)
	if err != nil {
		return err
	}

	source := FileMigrationSource{
		Dir: env.Dir,
	}

	if dryrun {
		migrations, err := PlanMigration(db, source, dir, limit)
		if err != nil {
			return fmt.Errorf("Cannot plan migration: %s", err)
		}

		for _, m := range migrations {
			PrintMigration(m, dir)
		}
	} else {
		n, err := ExecMax(db, source, dir, limit)
		if err != nil {
			return fmt.Errorf("Migration failed: %s", err)
		}

		if n == 1 {
			ui.Output("Applied 1 migration")
		} else {
			ui.Output(fmt.Sprintf("Applied %d migrations", n))
		}
	}

	return nil
}

func PrintMigration(m *PlannedMigration, dir MigrationDirection) {
	if dir == Up {
		ui.Output(fmt.Sprintf("==> Would apply migration %s (up)", m.Id))
		for _, q := range m.Up {
			ui.Output(q)
		}
	} else if dir == Down {
		ui.Output(fmt.Sprintf("==> Would apply migration %s (down)", m.Id))
		for _, q := range m.Down {
			ui.Output(q)
		}
	} else {
		panic("Not reached")
	}
}
