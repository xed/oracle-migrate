package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "github.com/mattn/go-oci8"
	"github.com/rubenv/sql-migrate/sqlparse"
)

type MigrationDirection int

const (
	Up MigrationDirection = iota
	Down
)

var tableName = "migration"
var schemaName = ""
var numberPrefixRegex = regexp.MustCompile(`^(\d+).*$`)

// PlanError happens where no migration plan could be created between the sets
// of already applied migrations and the currently found. For example, when the database
// contains a migration which is not among the migrations list found for an operation.
type PlanError struct {
	Migration    *Migration
	ErrorMessage string
}

func newPlanError(migration *Migration, errorMessage string) error {
	return &PlanError{
		Migration:    migration,
		ErrorMessage: errorMessage,
	}
}

func (p *PlanError) Error() string {
	return fmt.Sprintf("Unable to create migration plan because of %s: %s",
		p.Migration.Id, p.ErrorMessage)
}

// TxError is returned when any error is encountered during a database
// transaction. It contains the relevant *Migration and notes it's Id in the
// Error function output.
type TxError struct {
	Migration *Migration
	Err       error
}

func newTxError(migration *PlannedMigration, err error) error {
	return &TxError{
		Migration: migration.Migration,
		Err:       err,
	}
}

func (e *TxError) Error() string {
	return e.Err.Error() + " handling " + e.Migration.Id
}

// Set the name of the table used to store migration info.
//
// Should be called before any other call such as (Exec, ExecMax, ...).
func SetTable(name string) {
	if name != "" {
		tableName = name
	}
}

// SetSchema sets the name of a schema that the migration table be referenced.
func SetSchema(name string) {
	if name != "" {
		schemaName = name
	}
}

type Migration struct {
	Id   string
	Up   []string
	Down []string

	DisableTransactionUp   bool
	DisableTransactionDown bool
}

func (m Migration) Less(other *Migration) bool {
	switch {
	case m.isNumeric() && other.isNumeric() && m.VersionInt() != other.VersionInt():
		return m.VersionInt() < other.VersionInt()
	case m.isNumeric() && !other.isNumeric():
		return true
	case !m.isNumeric() && other.isNumeric():
		return false
	default:
		return m.Id < other.Id
	}
}

func (m Migration) isNumeric() bool {
	return len(m.NumberPrefixMatches()) > 0
}

func (m Migration) NumberPrefixMatches() []string {
	return numberPrefixRegex.FindStringSubmatch(m.Id)
}

func (m Migration) VersionInt() int64 {
	v := m.NumberPrefixMatches()[1]
	value, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		panic(fmt.Sprintf("Could not parse %q into int64: %s", v, err))
	}
	return value
}

type PlannedMigration struct {
	*Migration

	DisableTransaction bool
	Queries            []string
}

type byId []*Migration

func (b byId) Len() int           { return len(b) }
func (b byId) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }
func (b byId) Less(i, j int) bool { return b[i].Less(b[j]) }

type MigrationRecord struct {
	Id        string    `db:"id"`
	AppliedAt time.Time `db:"applied_at"`
}

type MigrationSource interface {
	// Finds the migrations.
	//
	// The resulting slice of migrations should be sorted by Id.
	FindMigrations() ([]*Migration, error)
}

// A hardcoded set of migrations, in-memory.
type MemoryMigrationSource struct {
	Migrations []*Migration
}

var _ MigrationSource = (*MemoryMigrationSource)(nil)

func (m MemoryMigrationSource) FindMigrations() ([]*Migration, error) {
	// Make sure migrations are sorted. In order to make the MemoryMigrationSource safe for
	// concurrent use we should not mutate it in place. So `FindMigrations` would sort a copy
	// of the m.Migrations.
	migrations := make([]*Migration, len(m.Migrations))
	copy(migrations, m.Migrations)
	sort.Sort(byId(migrations))
	return migrations, nil
}

// A set of migrations loaded from an http.FileServer

type HttpFileSystemMigrationSource struct {
	FileSystem http.FileSystem
}

var _ MigrationSource = (*HttpFileSystemMigrationSource)(nil)

func (f HttpFileSystemMigrationSource) FindMigrations() ([]*Migration, error) {
	return findMigrations(f.FileSystem)
}

// A set of migrations loaded from a directory.
type FileMigrationSource struct {
	Dir string
}

var _ MigrationSource = (*FileMigrationSource)(nil)

func (f FileMigrationSource) FindMigrations() ([]*Migration, error) {
	filesystem := http.Dir(f.Dir)
	return findMigrations(filesystem)
}

func findMigrations(dir http.FileSystem) ([]*Migration, error) {
	migrations := make([]*Migration, 0)

	file, err := dir.Open("/")
	if err != nil {
		return nil, err
	}

	files, err := file.Readdir(0)
	if err != nil {
		return nil, err
	}

	for _, info := range files {
		if strings.HasSuffix(info.Name(), ".sql") {
			file, err := dir.Open(info.Name())
			if err != nil {
				return nil, fmt.Errorf("Error while opening %s: %s", info.Name(), err)
			}

			migration, err := ParseMigration(info.Name(), file)
			if err != nil {
				return nil, fmt.Errorf("Error while parsing %s: %s", info.Name(), err)
			}

			migrations = append(migrations, migration)
		}
	}

	// Make sure migrations are sorted
	sort.Sort(byId(migrations))

	return migrations, nil
}

// Migrations from a bindata asset set.
type AssetMigrationSource struct {
	// Asset should return content of file in path if exists
	Asset func(path string) ([]byte, error)

	// AssetDir should return list of files in the path
	AssetDir func(path string) ([]string, error)

	// Path in the bindata to use.
	Dir string
}

var _ MigrationSource = (*AssetMigrationSource)(nil)

func (a AssetMigrationSource) FindMigrations() ([]*Migration, error) {
	migrations := make([]*Migration, 0)

	files, err := a.AssetDir(a.Dir)
	if err != nil {
		return nil, err
	}

	for _, name := range files {
		if strings.HasSuffix(name, ".sql") {
			file, err := a.Asset(path.Join(a.Dir, name))
			if err != nil {
				return nil, err
			}

			migration, err := ParseMigration(name, bytes.NewReader(file))
			if err != nil {
				return nil, err
			}

			migrations = append(migrations, migration)
		}
	}

	// Make sure migrations are sorted
	sort.Sort(byId(migrations))

	return migrations, nil
}

// Avoids pulling in the packr library for everyone, mimicks the bits of
// packr.Box that we need.
type PackrBox interface {
	List() []string
	Find(name string) ([]byte, error)
}

// Migrations from a packr box.
type PackrMigrationSource struct {
	Box PackrBox

	// Path in the box to use.
	Dir string
}

var _ MigrationSource = (*PackrMigrationSource)(nil)

func (p PackrMigrationSource) FindMigrations() ([]*Migration, error) {
	migrations := make([]*Migration, 0)
	items := p.Box.List()

	prefix := ""
	dir := path.Clean(p.Dir)
	if dir != "." {
		prefix = fmt.Sprintf("%s/", dir)
	}

	for _, item := range items {
		if !strings.HasPrefix(item, prefix) {
			continue
		}
		name := strings.TrimPrefix(item, prefix)
		if strings.Contains(name, "/") {
			continue
		}

		if strings.HasSuffix(name, ".sql") {
			file, err := p.Box.Find(item)
			if err != nil {
				return nil, err
			}

			migration, err := ParseMigration(name, bytes.NewReader(file))
			if err != nil {
				return nil, err
			}

			migrations = append(migrations, migration)
		}
	}

	// Make sure migrations are sorted
	sort.Sort(byId(migrations))

	return migrations, nil
}

// Migration parsing
func ParseMigration(id string, r io.ReadSeeker) (*Migration, error) {
	m := &Migration{
		Id: id,
	}

	parsed, err := sqlparse.ParseMigration(r)
	if err != nil {
		return nil, fmt.Errorf("Error parsing migration (%s): %s", id, err)
	}

	m.Up = parsed.UpStatements
	m.Down = parsed.DownStatements

	m.DisableTransactionUp = parsed.DisableTransactionUp
	m.DisableTransactionDown = parsed.DisableTransactionDown

	return m, nil
}

type SqlExecutor interface {
	Exec(query string, args ...interface{}) (sql.Result, error)
	Insert(list ...interface{}) error
	Delete(list ...interface{}) (int64, error)
}

// Execute a set of migrations
//
// Returns the number of applied migrations.
func Exec(db *sql.DB, dialect string, m MigrationSource, dir MigrationDirection) (int, error) {
	return ExecMax(db, m, dir, 0)
}

// Execute a set of migrations
//
// Will apply at most `max` migrations. Pass 0 for no limit (or use Exec).
//
// Returns the number of applied migrations.
func ExecMax(db *sql.DB, m MigrationSource, dir MigrationDirection, max int) (int, error) {
	migrations, err := PlanMigration(db, m, dir, max)
	if err != nil {
		return 0, err
	}
	// Apply migrations
	applied := 0

	for _, migration := range migrations {

		//TODO: @xed START transaction
		tx, err := db.Begin()
		if err != nil {
			fmt.Println("Problem with start transaction")
		}
		if tx != nil {
			for _, stmt := range migration.Queries {
				var query string
				query = strings.TrimSpace(stmt)

				last := query[len(query)-1:]
				if last == ";" {
					query = query[:len(query)-1]
				}

				fmt.Println("query", query)
				ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
				_, err = tx.ExecContext(ctx, query)
				if err != nil {
					fmt.Println("ExecMax ExecContext error is not nil:", err)
					err := tx.Rollback()
					if err != nil {
						fmt.Println("ffffffff")
					}
					return applied, newTxError(migration, err)
				}
				cancel()
			}

			switch dir {
			case Up:
				query := "insert into " + tableName + " ( id, applied_at) values (:1,:2)"
				ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
				result, err := tx.ExecContext(ctx, query, migration.Id, time.Now())
				cancel()
				fmt.Println("in ExecMax time", time.Now())
				fmt.Println("in ExecMax result ", result, " err", err, " query:", query)
				if err != nil {
					log.Println(err.Error())
					err := tx.Rollback()
					if err != nil {
						fmt.Println("ffffffff")
					}
					return applied, newTxError(migration, err)
				}
			case Down:
				query := "DELETE FROM " + tableName + " WHERE id = :1"
				fmt.Println("query down", query)
				ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
				_, err = tx.ExecContext(ctx, query, migration.Id)
				cancel()
				if err != nil {
					err := tx.Rollback()
					if err != nil {
						fmt.Println("ffffffff")
					}
					fmt.Println("ExecContext error is not nil:", err)
					return applied, newTxError(migration, err)
				}
			default:
				err := tx.Rollback()
				if err != nil {
					fmt.Println("ffffffff")
				}
				panic("Not possible")
			}
			err = tx.Commit()
			if err != nil {
				fmt.Println("Commit doesnt work", applied)
				return applied, nil
			}
		}

	}

	applied++

	fmt.Println("applied", applied)
	return applied, nil
}

func PlanMigration(db *sql.DB, m MigrationSource, dir MigrationDirection, max int) ([]*PlannedMigration, error) {
	err := checkMigrationTable(db)
	if err != nil {
		fmt.Println("err != nil in PlanMigration with checkMigrationTable")
	}

	migrations, err := m.FindMigrations()
	if err != nil {
		return nil, err
	}

	migrationRecords, err := getMigrationRecords(db)
	if err != nil {
		return nil, err
	}
	// Sort migrations that have been run by Id.
	var existingMigrations []*Migration
	for _, migrationRecord := range migrationRecords {
		fmt.Println("getMigrationRecords in for", migrationRecord.Id)
		existingMigrations = append(existingMigrations, &Migration{
			Id: migrationRecord.Id,
		})
	}
	sort.Sort(byId(existingMigrations))
	// Make sure all migrations in the database are among the found migrations which
	// are to be applied.
	migrationsSearch := make(map[string]struct{})
	for _, migration := range migrations {
		migrationsSearch[migration.Id] = struct{}{}
	}
	for _, existingMigration := range existingMigrations {
		fmt.Println("existingMigration", existingMigration.Id)
		if _, ok := migrationsSearch[existingMigration.Id]; !ok {
			return nil, newPlanError(existingMigration, "unknown migration in database")
		}
	}

	// Get last migration that was run
	record := &Migration{}
	if len(existingMigrations) > 0 {
		record = existingMigrations[len(existingMigrations)-1]
	}

	result := make([]*PlannedMigration, 0)

	// Add missing migrations up to the last run migration.
	// This can happen for example when merges happened.
	if len(existingMigrations) > 0 {
		result = append(result, ToCatchup(migrations, existingMigrations, record)...)
	}

	// Figure out which migrations to apply
	toApply := ToApply(migrations, record.Id, dir)
	toApplyCount := len(toApply)
	if max > 0 && max < toApplyCount {
		toApplyCount = max
	}
	for _, v := range toApply[0:toApplyCount] {
		if dir == Up {
			result = append(result, &PlannedMigration{
				Migration:          v,
				Queries:            v.Up,
				DisableTransaction: v.DisableTransactionUp,
			})
		} else if dir == Down {
			result = append(result, &PlannedMigration{
				Migration:          v,
				Queries:            v.Down,
				DisableTransaction: v.DisableTransactionDown,
			})
		}
	}
	fmt.Println("PlanMigration before return result")
	return result, nil
}

// Plan a migration.
//func PlanMigration(db *sql.DB, m MigrationSource, dir MigrationDirection, max int) ([]*PlannedMigration, error) {
//	err := checkMigrationTable(db)
//	if err != nil {
//		fmt.Println("err != nil in PlanMigration with checkMigrationTable")
//	}
//
//	migrations, err := m.FindMigrations()
//	if err != nil {
//		return nil, err
//	}
//	fmt.Println("PlanMigration migrations", migrations)
//	migrationRecords, err := getMigrationRecords(db)
//	if err != nil {
//		return nil, err
//	}
//	fmt.Println("PlanMigration migrationRecords", migrationRecords)
//	// Sort migrations that have been run by Id.
//	var existingMigrations []*Migration
//	for _, migrationRecord := range migrationRecords {
//		fmt.Println("migrationRecord", migrationRecord.Id)
//		existingMigrations = append(existingMigrations, &Migration{
//			Id: migrationRecord.Id,
//		})
//	}
//	sort.Sort(byId(existingMigrations))
//	fmt.Println("PlanMigration existingMigrations", existingMigrations)
//	// Make sure all migrations in the database are among the found migrations which
//	// are to be applied.
//	migrationsSearch := make(map[string]struct{})
//	for _, migration := range migrations {
//		migrationsSearch[migration.Id] = struct{}{}
//	}
//	for _, existingMigration := range existingMigrations {
//		fmt.Println("existingMigration", existingMigration.Id)
//		if _, ok := migrationsSearch[existingMigration.Id]; !ok {
//			return nil, newPlanError(existingMigration, "unknown migration in database")
//		}
//	}
//
//	// Get last migration that was run
//	record := &Migration{}
//	if len(existingMigrations) > 0 {
//		record = existingMigrations[len(existingMigrations)-1]
//	}
//
//	result := make([]*PlannedMigration, 0)
//
//	// Add missing migrations up to the last run migration.
//	// This can happen for example when merges happened.
//	if len(existingMigrations) > 0 {
//		result = append(result, ToCatchup(migrations, existingMigrations, record)...)
//	}
//
//	// Figure out which migrations to apply
//	toApply := ToApply(migrations, record.Id, dir)
//	toApplyCount := len(toApply)
//	if max > 0 && max < toApplyCount {
//		toApplyCount = max
//	}
//	for _, v := range toApply[0:toApplyCount] {
//		if dir == Up {
//			result = append(result, &PlannedMigration{
//				Migration:          v,
//				Queries:            v.Up,
//				DisableTransaction: v.DisableTransactionUp,
//			})
//		} else if dir == Down {
//			result = append(result, &PlannedMigration{
//				Migration:          v,
//				Queries:            v.Down,
//				DisableTransaction: v.DisableTransactionDown,
//			})
//		}
//	}
//	fmt.Println("PlanMigration before return result")
//	return result, nil
//}

// Skip a set of migrations
//
// Will skip at most `max` migrations. Pass 0 for no limit.
//
// Returns the number of skipped migrations.
//func SkipMax(db *sql.DB, m MigrationSource, dir MigrationDirection, max int) (int, error) {
//	migrations, err := PlanMigration(db, m, dir, max)
//	if err != nil {
//		return 0, err
//	}
//
//	// Skip migrations
//	applied := 0
//	for _, migration := range migrations {
//		var executor SqlExecutor
//
//		if migration.DisableTransaction {
//			executor = dbMap
//		} else {
//			executor, err = dbMap.Begin()
//			if err != nil {
//				return applied, newTxError(migration, err)
//			}
//		}
//
//		err = executor.Insert(&MigrationRecord{
//			Id:        migration.Id,
//			AppliedAt: time.Now(),
//		})
//		if err != nil {
//			if trans, ok := executor.(*gorp.Transaction); ok {
//				_ = trans.Rollback()
//			}
//
//			return applied, newTxError(migration, err)
//		}
//
//		if trans, ok := executor.(*gorp.Transaction); ok {
//			if err := trans.Commit(); err != nil {
//				return applied, newTxError(migration, err)
//			}
//		}
//
//		applied++
//	}
//
//	return applied, nil
//}

// Filter a slice of migrations into ones that should be applied.
func ToApply(migrations []*Migration, current string, direction MigrationDirection) []*Migration {
	var index = -1
	if current != "" {
		for index < len(migrations)-1 {
			index++
			if migrations[index].Id == current {
				break
			}
		}
	}

	if direction == Up {
		return migrations[index+1:]
	} else if direction == Down {
		if index == -1 {
			return []*Migration{}
		}

		// Add in reverse order
		toApply := make([]*Migration, index+1)
		for i := 0; i < index+1; i++ {
			toApply[index-i] = migrations[i]
		}
		return toApply
	}

	panic("Not possible")
}

func ToCatchup(migrations, existingMigrations []*Migration, lastRun *Migration) []*PlannedMigration {
	missing := make([]*PlannedMigration, 0)
	for _, migration := range migrations {
		found := false
		for _, existing := range existingMigrations {
			if existing.Id == migration.Id {
				found = true
				break
			}
		}
		if !found && migration.Less(lastRun) {
			missing = append(missing, &PlannedMigration{
				Migration:          migration,
				Queries:            migration.Up,
				DisableTransaction: migration.DisableTransactionUp,
			})
		}
	}
	return missing
}

//func GetMigrationRecords(db *sql.DB, dialect string) ([]*MigrationRecord, error) {
//	dbMap, err := getMigrationDbMap(db, dialect)
//	if err != nil {
//		return nil, err
//	}
//
//	var records []*MigrationRecord
//	query := fmt.Sprintf("SELECT * FROM %s ORDER BY id ASC", dbMap.Dialect.QuotedTableForQuery(schemaName, tableName))
//	_, err = dbMap.Select(&records, query)
//	if err != nil {
//		return nil, err
//	}
//
//	return records, nil
//}

//func getMigrationDbMap(db *sql.DB, dialect string) (*gorp.DbMap, error) {
//	d, ok := MigrationDialects[dialect]
//	if !ok {
//		return nil, fmt.Errorf("Unknown dialect: %s", dialect)
//	}
//
//	// When using the mysql driver, make sure that the parseTime option is
//	// configured, otherwise it won't map time columns to time.Time. See
//	// https://github.com/rubenv/sql-migrate/issues/2
//	if dialect == "mysql" {
//		var out *time.Time
//		err := db.QueryRow("SELECT NOW()").Scan(&out)
//		if err != nil {
//			if err.Error() == "sql: Scan error on column index 0: unsupported driver -> Scan pair: []uint8 -> *time.Time" ||
//				err.Error() == "sql: Scan error on column index 0: unsupported Scan, storing driver.Value type []uint8 into type *time.Time" ||
//				err.Error() == "sql: Scan error on column index 0, name \"NOW()\": unsupported Scan, storing driver.Value type []uint8 into type *time.Time" {
//				return nil, errors.New(`Cannot parse dates.
//
//Make sure that the parseTime option is supplied to your database connection.
//Check https://github.com/go-sql-driver/mysql#parsetime for more info.`)
//			} else {
//				return nil, err
//			}
//		}
//	}
//
//	// Create migration database map
//	dbMap := &gorp.DbMap{Db: db, Dialect: d}
//	dbMap.AddTableWithNameAndSchema(MigrationRecord{}, schemaName, tableName).SetKeys(false, "Id")
//	//dbMap.TraceOn("", log.New(os.Stdout, "migrate: ", log.Lmicroseconds))
//
//	err := dbMap.CreateTablesIfNotExists()
//	if err != nil {
//		return nil, err
//	}
//
//	return dbMap, nil
//}

// TODO: Run migration + record insert in transaction.

func checkMigrationTable(db *sql.DB) error {
	_, err := db.Query(fmt.Sprintf("select * from %s", tableName))
	if err != nil && err != sql.ErrNoRows {
		fmt.Println("Empty row in result for select migration")
		query := "CREATE TABLE " + tableName + " (id varchar(255),applied_at timestamp with time zone)"
		ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
		_, err = db.ExecContext(ctx, query)
		cancel()
		if err != nil {
			fmt.Println("ExecContext error is not nil:", err)
			return err
		}
	} else {
		fmt.Println("Not empty row in checkMigrationTable")
	}

	return nil
}

func getMigrationRecords(db *sql.DB) ([]MigrationRecord, error) {
	var migrationRecords []MigrationRecord

	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	defer cancel()
	rows, err := db.QueryContext(ctx, fmt.Sprintf("SELECT * FROM %s ORDER BY id ASC", tableName))
	if err != nil {
		fmt.Println("QueryContext error is not nil:", err)
		return nil, err
	}
	for rows.Next() {
		var id string
		var appliedAt time.Time
		if err := rows.Scan(&id, &appliedAt); err != nil {
			log.Println(err.Error())
		}
		migrationRecords = append(migrationRecords, MigrationRecord{
			Id:        id,
			AppliedAt: appliedAt,
		})
	}
	err = rows.Err()
	if err != nil {
		fmt.Println("Err error is not nil:", err)
		return nil, err
	}
	err = rows.Close()
	if err != nil {
		fmt.Println("Close error is not nil:", err)
		return nil, err
	}
	return migrationRecords, err
}
