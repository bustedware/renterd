package stores

import (
	"fmt"
	"os"
	"time"

	"go.sia.tech/siad/modules"
	"gorm.io/driver/mysql"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	glogger "gorm.io/gorm/logger"
)

type (
	// Model defines the common fields of every table. Same as Model
	// but excludes soft deletion since it breaks cascading deletes.
	Model struct {
		ID        uint `gorm:"primarykey"`
		CreatedAt time.Time
		UpdatedAt time.Time
	}

	// SQLStore is a helper type for interacting with a SQL-based backend.
	SQLStore struct {
		db *gorm.DB

		// HostDB related fields.
		lastAnnouncementSave   time.Time
		persistInterval        time.Duration
		unappliedAnnouncements []announcement
		unappliedCCID          modules.ConsensusChangeID
	}
)

// NewEphemeralSQLiteConnection creates a connection to an in-memory SQLite DB.
// NOTE: Use simple names such as a random hex identifier or the filepath.Base
// of a test's name. Certain symbols will break the cfg string and cause a file
// to be created on disk.
//
//	mode: set to memory for in-memory database
//	cache: set to shared which is required for in-memory databases
//	_foreign_keys: enforce foreign_key relations
func NewEphemeralSQLiteConnection(name string) gorm.Dialector {
	return sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared&_foreign_keys=1", name))
}

// NewSQLiteConnection opens a sqlite db at the given path.
//
//	_busy_timeout: set to prevent concurrent transactions from failing and
//	  instead have them block
//	_foreign_keys: enforce foreign_key relations
//	_journal_mode: set to WAL instead of delete since it's usually the fastest.
//	  Only downside is that the db won't work on network drives. In that case this
//	  should be made configurable and set to TRUNCATE or any of the other options.
//	  For reference see https://github.com/mattn/go-sqlite3#connection-string.
func NewSQLiteConnection(path string) gorm.Dialector {
	return sqlite.Open(fmt.Sprintf("file:%s?_busy_timeout=30000&_foreign_keys=1&_journal_mode=WAL", path))
}

// NewMySQLConnection creates a connection to a MySQL database.
func NewMySQLConnection(user, password, addr, dbName string) gorm.Dialector {
	fmt.Println("dsn", fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=True&loc=Local", user, password, addr, dbName))
	return mysql.Open(fmt.Sprintf("%s:%s@tcp(%s)/%s?charset=utf8mb4&parseTime=True&loc=Local", user, password, addr, dbName))
}

func DBConfigFromEnv() (uri, user, password, dbName string) {
	uri = os.Getenv("RENTER_DB_URI")
	user = os.Getenv("RENTER_DB_USER")
	password = os.Getenv("RENTER_DB_PASSWORD")
	dbName = os.Getenv("RENTER_DB_NAME")
	return
}

// NewSQLStore uses a given Dialector to connect to a SQL database.  NOTE: Only
// pass migrate=true for the first instance of SQLHostDB if you connect via the
// same Dialector multiple times.
func NewSQLStore(conn gorm.Dialector, migrate bool, persistInterval time.Duration, logger glogger.Interface) (*SQLStore, modules.ConsensusChangeID, error) {
	db, err := gorm.Open(conn, &gorm.Config{
		DisableNestedTransaction: true,   // disable nesting transactions
		PrepareStmt:              true,   // caches queries as prepared statements
		Logger:                   logger, // custom logger
	})
	if err != nil {
		return nil, modules.ConsensusChangeID{}, err
	}

	if migrate {
		// Create the tables.
		tables := []interface{}{
			// bus.MetadataStore tables
			&dbArchivedContract{},
			&dbContract{},
			&dbContractSet{},
			&dbObject{},
			&dbSector{},
			&dbShard{},
			&dbSlab{},
			&dbSlice{},

			// bus.HostDB tables
			&dbAnnouncement{},
			&dbConsensusInfo{},
			&dbHost{},
			&dbInteraction{},
			&dbBlocklistEntry{},

			// bus.SettingStore tables
			&dbSetting{},

			// bus.EphemeralAccountStore tables
			&dbAccount{},
		}
		if err := db.AutoMigrate(tables...); err != nil {
			return nil, modules.ConsensusChangeID{}, err
		}
	}

	// Ensure the join table has an index on `db_host_id`.
	switch conn.(type) {
	case sqlite.Dialector:
		if err := db.Exec("CREATE INDEX IF NOT EXIST idx_host_blocklist_entry_hosts ON host_blocklist_entry_hosts (db_host_id)").Error; err != nil {
			return nil, modules.ConsensusChangeID{}, err
		}
	case mysql.Dialector:
		indexFound := db.Exec("SELECT COUNT(1) IndexIsThere FROM INFORMATION_SCHEMA.STATISTICS WHERE table_schema=DATABASE() AND table_name='host_blocklist_entry_hosts' AND index_name='idx_host_blocklist_entry_hosts'").RowsAffected
		if indexFound == 0 {
			if err := db.Exec("CREATE INDEX idx_host_blocklist_entry_hosts ON host_blocklist_entry_hosts (db_host_id)").Error; err != nil {
				return nil, modules.ConsensusChangeID{}, err
			}
		}
	}

	// Get latest consensus change ID or init db.
	var ci dbConsensusInfo
	err = db.Where(&dbConsensusInfo{Model: Model{ID: consensusInfoID}}).
		Attrs(dbConsensusInfo{
			Model: Model{
				ID: consensusInfoID,
			},
			CCID: modules.ConsensusChangeBeginning[:],
		}).
		FirstOrCreate(&ci).Error
	if err != nil {
		return nil, modules.ConsensusChangeID{}, err
	}
	var ccid modules.ConsensusChangeID
	copy(ccid[:], ci.CCID)

	ss := &SQLStore{
		db:                   db,
		lastAnnouncementSave: time.Now(),
		persistInterval:      persistInterval,
	}
	return ss, ccid, nil
}

// Close closes the underlying database connection of the store.
func (s *SQLStore) Close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}
