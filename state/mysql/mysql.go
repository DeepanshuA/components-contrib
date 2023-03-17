/*
Copyright 2021 The Dapr Authors
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at
    http://www.apache.org/licenses/LICENSE-2.0
Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mysql

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	sqlCleanup "github.com/dapr/components-contrib/internal/component/sql"
	"github.com/dapr/components-contrib/metadata"
	"github.com/dapr/components-contrib/state"
	"github.com/dapr/components-contrib/state/utils"
	"github.com/dapr/kit/logger"
	"github.com/dapr/kit/ptr"
)

// Optimistic Concurrency is implemented using a string column that stores a UUID.

const (
	// The key name in the metadata for the timeout of operations, in seconds.
	keyTimeoutInSeconds = "timeoutInSeconds"

	// To connect to MySQL running in Azure over SSL you have to download a
	// SSL certificate. If this is provided the driver will connect using
	// SSL. If you have disable SSL you can leave this empty.
	// When the user provides a pem path their connection string must end with
	// &tls=custom
	// The connection string should be in the following format
	// "%s:%s@tcp(%s:3306)/%s?allowNativePasswords=true&tls=custom",'myadmin@mydemoserver', 'yourpassword', 'mydemoserver.mysql.database.azure.com', 'targetdb'.
	keyPemPath = "pemPath"

	// Used if the user does not configure a table name in the metadata.
	defaultTableName = "state"

	// Used if the user does not configure a database name in the metadata.
	defaultSchemaName = "dapr_state_store"

	// Used if the user does not provide a timeoutInSeconds value in the metadata.
	defaultTimeoutInSeconds = 20

	// Standard error message if not connection string is provided.
	errMissingConnectionString = "missing connection string"

	// Key name to configure interval at which entries with TTL are cleaned up.
	// The value is in seconds.
	cleanupIntervalKey = "cleanupIntervalInSeconds"

	// Used if the user does not configure a metadata table name in the metadata.
	// In terms of TTL, it is required to store value for 'last-cleanup' id.
	defaultMetadataTableName = "dapr_metadata"

	// Used if the user does not configure a cleanup interval in the metadata.
	defaultCleanupInterval = 3600
)

// MySQL state store.
type MySQL struct {
	tableName         string
	metadataTableName string
	cleanupInterval   *time.Duration
	schemaName        string
	connectionString  string
	timeout           time.Duration

	// Instance of the database to issue commands to
	db *sql.DB

	// Logger used in a functions
	logger logger.Logger

	factory iMySQLFactory
	gc      sqlCleanup.GarbageCollector
}

type mySQLMetadata struct {
	TableName         string
	SchemaName        string
	ConnectionString  string
	Timeout           int
	PemPath           string
	MetadataTableName string
	CleanupInterval   *time.Duration
}

// NewMySQLStateStore creates a new instance of MySQL state store.
func NewMySQLStateStore(logger logger.Logger) state.Store {
	factory := newMySQLFactory(logger)

	// Store the provided logger and return the object. The rest of the
	// properties will be populated in the Init function
	return newMySQLStateStore(logger, factory)
}

// Hidden implementation for testing.
func newMySQLStateStore(logger logger.Logger, factory iMySQLFactory) *MySQL {
	// Store the provided logger and return the object. The rest of the
	// properties will be populated in the Init function
	return &MySQL{
		logger:  logger,
		factory: factory,
		timeout: 5 * time.Second,
	}
}

// Init initializes the SQL server state store
// Implements the following interfaces:
// Store
// TransactionalStore
// Populate the rest of the MySQL object by reading the metadata and opening
// a connection to the server.
func (m *MySQL) Init(ctx context.Context, metadata state.Metadata) error {
	m.logger.Debug("Initializing MySql state store")

	err := m.parseMetadata(metadata.Properties)
	if err != nil {
		return err
	}

	db, err := m.factory.Open(m.connectionString)
	if err != nil {
		m.logger.Error(err)
		return err
	}

	// will be nil if everything is good or an err that needs to be returned
	return m.finishInit(ctx, db)
}

func (m *MySQL) parseMetadata(md map[string]string) error {
	meta := mySQLMetadata{
		TableName:         defaultTableName,
		SchemaName:        defaultSchemaName,
		MetadataTableName: defaultMetadataTableName,
		CleanupInterval:   ptr.Of(defaultCleanupInterval * time.Second),
	}
	fmt.Printf("CleanupIntervalInSeconds at 1 is %v", meta.CleanupInterval)
	err := metadata.DecodeMetadata(md, &meta)
	if err != nil {
		return err
	}
	fmt.Printf("CleanupIntervalInSeconds at 2 is %v", meta.CleanupInterval)
	if meta.TableName != "" {
		// Sanitize the table name
		if !validIdentifier(meta.TableName) {
			return fmt.Errorf("table name '%s' is not valid", meta.TableName)
		}
	}
	m.tableName = meta.TableName

	if meta.MetadataTableName != "" {
		// Sanitize the metadata table name
		if !validIdentifier(meta.MetadataTableName) {
			return fmt.Errorf("metadata table name '%s' is not valid", meta.MetadataTableName)
		}
	}
	m.metadataTableName = meta.MetadataTableName

	if meta.SchemaName != "" {
		// Sanitize the schema name
		if !validIdentifier(meta.SchemaName) {
			return fmt.Errorf("schema name '%s' is not valid", meta.SchemaName)
		}
	}
	m.schemaName = meta.SchemaName

	if meta.ConnectionString == "" {
		m.logger.Error("Missing MySql connection string")
		return fmt.Errorf(errMissingConnectionString)
	}
	m.connectionString = meta.ConnectionString

	// Cleanup interval
	s, ok := md[cleanupIntervalKey]
	if ok && s != "" {
		cleanupIntervalInSec, err := strconv.ParseInt(s, 10, 0)
		if err != nil {
			return fmt.Errorf("invalid value for '%s': %s", cleanupIntervalKey, s)
		}

		// Non-positive value from meta means disable auto cleanup.
		if cleanupIntervalInSec > 0 {
			m.cleanupInterval = ptr.Of(time.Duration(cleanupIntervalInSec) * time.Second)
		} else {
			m.cleanupInterval = nil
		}
		fmt.Printf("CleanupIntervalInSeconds at 4 is %v", meta.CleanupInterval)
	} else {
		m.cleanupInterval = meta.CleanupInterval
		fmt.Printf("cleanupInterval at 3 is %v", m.cleanupInterval)
	}

	if meta.PemPath != "" {
		err := m.factory.RegisterTLSConfig(meta.PemPath)
		if err != nil {
			m.logger.Error(err)
			return err
		}
	}

	val, ok := md[keyTimeoutInSeconds]
	if ok && val != "" {
		n, err := strconv.Atoi(val)
		if err == nil && n > 0 {
			m.timeout = time.Duration(n) * time.Second
		}
	}
	if m.timeout <= 0 {
		m.timeout = time.Duration(defaultTimeoutInSeconds) * time.Second
	}

	return nil
}

// Features returns the features available in this state store.
func (m *MySQL) Features() []state.Feature {
	return []state.Feature{state.FeatureETag, state.FeatureTransactional}
}

// Ping the database.
func (m *MySQL) Ping(ctx context.Context) error {
	if m.db == nil {
		return sql.ErrConnDone
	}

	return m.PingWithContext(ctx)
}

// PingWithContext is like Ping but accepts a context.
func (m *MySQL) PingWithContext(parentCtx context.Context) error {
	ctx, cancel := context.WithTimeout(parentCtx, m.timeout)
	defer cancel()
	return m.db.PingContext(ctx)
}

// Separated out to make this portion of code testable.
func (m *MySQL) finishInit(ctx context.Context, db *sql.DB) error {
	m.db = db

	err := m.ensureStateSchema(ctx)
	if err != nil {
		m.logger.Error(err)
		return err
	}

	err = m.Ping(ctx)
	if err != nil {
		m.logger.Error(err)
		return err
	}

	// will be nil if everything is good or an err that needs to be returned
	if err = m.ensureStateTable(ctx, m.schemaName, m.tableName); err != nil {
		return err
	}

	if err = m.ensureMetadataTable(ctx, m.schemaName, m.metadataTableName); err != nil {
		return err
	}

	if m.cleanupInterval != nil {
		gc, err := sqlCleanup.ScheduleGarbageCollector(sqlCleanup.GCOptions{
			Logger: m.logger,
			UpdateLastCleanupQuery: fmt.Sprintf(`INSERT INTO %[1]s (id, value)
			VALUES ('last-cleanup', CURRENT_TIMESTAMP)
		  ON DUPLICATE KEY UPDATE
			value = IF((UNIX_TIMESTAMP(CURRENT_TIMESTAMP) - UNIX_TIMESTAMP(value)) * 1000 >= ?, 
					  CURRENT_TIMESTAMP, value)`,
				m.metadataTableName),
			DeleteExpiredValuesQuery: fmt.Sprintf(
				`DELETE FROM %s WHERE expiredate IS NOT NULL AND expiredate < CURRENT_TIMESTAMP`,
				m.tableName,
			),
			CleanupInterval: *m.cleanupInterval,
			DBSql:           m.db,
		})
		if err != nil {
			return err
		}
		m.gc = gc
	}
	return nil
}

func (m *MySQL) ensureStateSchema(ctx context.Context) error {
	exists, err := schemaExists(ctx, m.db, m.schemaName, m.timeout)
	if err != nil {
		return err
	}

	if !exists {
		m.logger.Infof("Creating MySql schema '%s'", m.schemaName)
		cctx, cancel := context.WithTimeout(ctx, m.timeout)
		defer cancel()
		_, err = m.db.ExecContext(cctx,
			fmt.Sprintf("CREATE DATABASE %s;", m.schemaName),
		)
		if err != nil {
			return err
		}
	}

	// Build a connection string that contains the new schema name
	// All MySQL connection strings must contain a / so split on it.
	parts := strings.Split(m.connectionString, "/")

	// Even if the connection string ends with a / parts will have two values
	// with the second being an empty string.
	m.connectionString = fmt.Sprintf("%s/%s%s", parts[0], m.schemaName, parts[1])

	// Close the connection we used to confirm and or create the schema
	err = m.db.Close()

	if err != nil {
		return err
	}

	// Open a connection to the new schema
	m.db, err = m.factory.Open(m.connectionString)

	return err
}

func (m *MySQL) ensureStateTable(ctx context.Context, schemaName, stateTableName string) error {
	tableExists, err := tableExists(ctx, m.db, schemaName, stateTableName, m.timeout)
	if err != nil {
		return err
	}

	if !tableExists {
		m.logger.Infof("Creating MySql state table '%s'", stateTableName)

		// updateDate is updated automactically on every UPDATE commands so you
		// never need to pass it in.
		// eTag is a UUID stored as a 36 characters string. It needs to be passed
		// in on inserts and updates and is used for Optimistic Concurrency
		// Note that stateTableName is sanitized
		//nolint:gosec
		createTable := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id VARCHAR(255) NOT NULL PRIMARY KEY,
			value JSON NOT NULL,
			isbinary BOOLEAN NOT NULL,
			insertDate TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updateDate TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			eTag VARCHAR(36) NOT NULL,
			expiredate TIMESTAMP NULL,
			INDEX expiredate_idx(expiredate)
			);`, stateTableName)

		execCtx, execCancel := context.WithTimeout(ctx, m.timeout)
		defer execCancel()
		_, err = m.db.ExecContext(execCtx, createTable)
		if err != nil {
			return err
		}
	}

	// Check if expiredate column exists - to cater cases when table was created before v1.11.
	columnExists, err := columnExists(ctx, m.db, schemaName, stateTableName, "expiredate", m.timeout)
	if err != nil {
		return err
	}

	if !columnExists {
		m.logger.Infof("Adding expiredate column to MySql state table '%s'", stateTableName)
		_, err = m.db.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN expiredate TIMESTAMP NULL;`, stateTableName))
		if err != nil {
			return err
		}
		_, err = m.db.ExecContext(ctx, fmt.Sprintf(
			`CREATE INDEX expiredate_idx ON %s (expiredate);`, stateTableName))
		if err != nil {
			return err
		}
	}

	return nil
}

func (m *MySQL) ensureMetadataTable(ctx context.Context, schemaName, metaTableName string) error {
	exists, err := tableExists(ctx, m.db, schemaName, metaTableName, m.timeout)
	if err != nil {
		return err
	}

	if !exists {
		m.logger.Info("Creating MySQL metadata table")
		_, err = m.db.ExecContext(ctx, fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
			id VARCHAR(255) NOT NULL PRIMARY KEY, value VARCHAR(255) NOT NULL);`, metaTableName))
		if err != nil {
			return err
		}
	}

	return nil
}

func schemaExists(ctx context.Context, db *sql.DB, schemaName string, timeout time.Duration) (bool, error) {
	schemeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Returns 1 or 0 if the table exists or not
	var exists int
	query := `SELECT EXISTS (
		SELECT SCHEMA_NAME FROM information_schema.schemata WHERE SCHEMA_NAME = ?
	) AS 'exists'`
	err := db.QueryRowContext(schemeCtx, query, schemaName).Scan(&exists)
	return exists == 1, err
}

func tableExists(ctx context.Context, db *sql.DB, schemaName, tableName string, timeout time.Duration) (bool, error) {
	tableCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Returns 1 or 0 if the table exists or not
	var exists int
	query := `SELECT EXISTS (
		SELECT TABLE_NAME FROM information_schema.tables WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
	) AS 'exists'`
	err := db.QueryRowContext(tableCtx, query, schemaName, tableName).Scan(&exists)
	return exists == 1, err
}

// columnExists returns true if the column exists in the table
func columnExists(ctx context.Context, db *sql.DB, schemaName string, tableName string, columnName string, timeout time.Duration) (bool, error) {
	columnCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Returns 1 or 0 if the column exists or not
	var exists int
	query := `SELECT count(*) AS 'exists' FROM information_schema.columns
	WHERE table_schema = ?
	  AND table_name = ?
	  AND column_name = ?`
	err := db.QueryRowContext(columnCtx, query, schemaName, tableName, columnName).Scan(&exists)
	return exists == 1, err
}

// Delete removes an entity from the store
// Store Interface.
func (m *MySQL) Delete(ctx context.Context, req *state.DeleteRequest) error {
	return m.deleteValue(ctx, m.db, req)
}

// deleteValue is an internal implementation of delete to enable passing the
// logic to state.DeleteWithRetries as a func.
func (m *MySQL) deleteValue(parentCtx context.Context, querier querier, req *state.DeleteRequest) error {
	m.logger.Debug("Deleting state value from MySql")

	if req.Key == "" {
		return fmt.Errorf("missing key in delete operation")
	}

	var (
		err    error
		result sql.Result
	)

	execCtx, cancel := context.WithTimeout(parentCtx, m.timeout)
	defer cancel()

	if req.ETag == nil || *req.ETag == "" {
		result, err = querier.ExecContext(execCtx, fmt.Sprintf(
			`DELETE FROM %s WHERE id = ?`,
			m.tableName), req.Key)
	} else {
		result, err = querier.ExecContext(execCtx, fmt.Sprintf(
			`DELETE FROM %s WHERE id = ? and eTag = ?`,
			m.tableName), req.Key, *req.ETag)
	}

	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rows != 1 && req.ETag != nil && *req.ETag != "" {
		return state.NewETagError(state.ETagMismatch, nil)
	}

	return nil
}

// BulkDelete removes multiple entries from the store
// Store Interface.
func (m *MySQL) BulkDelete(ctx context.Context, req []state.DeleteRequest) error {
	m.logger.Debug("Executing BulkDelete request")

	tx, err := m.db.Begin()
	if err != nil {
		return err
	}

	if len(req) > 0 {
		for _, d := range req {
			da := d // Fix for goSec G601: Implicit memory aliasing in for loop.
			err = m.deleteValue(ctx, tx, &da)
			if err != nil {
				rollbackErr := tx.Rollback()
				if rollbackErr != nil {
					m.logger.Errorf("Error rolling back transaction: %v", rollbackErr)
				}
				return err
			}
		}
	}

	err = tx.Commit()

	return err
}

// Get returns an entity from store
// Store Interface.
func (m *MySQL) Get(parentCtx context.Context, req *state.GetRequest) (*state.GetResponse, error) {
	m.logger.Debug("Getting state value from MySql")

	if req.Key == "" {
		return nil, fmt.Errorf("missing key in get operation")
	}

	var (
		eTag     string
		value    []byte
		isBinary bool
	)

	ctx, cancel := context.WithTimeout(parentCtx, m.timeout)
	defer cancel()
	//nolint:gosec
	query := fmt.Sprintf(
		`SELECT value, eTag, isbinary FROM %s WHERE id = ?
			AND (expiredate IS NULL OR expiredate > CURRENT_TIMESTAMP)`,
		m.tableName, // m.tableName is sanitized
	)
	err := m.db.QueryRowContext(ctx, query, req.Key).Scan(&value, &eTag, &isBinary)
	if err != nil {
		// If no rows exist, return an empty response, otherwise return an error.
		if errors.Is(err, sql.ErrNoRows) {
			return &state.GetResponse{}, nil
		}

		return nil, err
	}

	if isBinary {
		var (
			s    string
			data []byte
		)

		err = json.Unmarshal(value, &s)
		if err != nil {
			return nil, err
		}

		data, err = base64.StdEncoding.DecodeString(s)
		if err != nil {
			return nil, err
		}

		return &state.GetResponse{
			Data:     data,
			ETag:     &eTag,
			Metadata: req.Metadata,
		}, nil
	}

	return &state.GetResponse{
		Data:     value,
		ETag:     &eTag,
		Metadata: req.Metadata,
	}, nil
}

// Set adds/updates an entity on store
// Store Interface.
func (m *MySQL) Set(ctx context.Context, req *state.SetRequest) error {
	return m.setValue(ctx, m.db, req)
}

// setValue is an internal implementation of set to enable passing the logic
// to state.SetWithRetries as a func.
func (m *MySQL) setValue(parentCtx context.Context, querier querier, req *state.SetRequest) error {
	m.logger.Debug("Setting state value in MySql")

	err := state.CheckRequestOptions(req.Options)
	if err != nil {
		return err
	}

	if req.Key == "" {
		return errors.New("missing key in set operation")
	}

	// TTL
	var ttlSeconds int
	ttl, ttlerr := utils.ParseTTL(req.Metadata)
	if ttlerr != nil {
		return fmt.Errorf("error parsing TTL: %w", ttlerr)
	}
	if ttl != nil {
		ttlSeconds = *ttl
	}

	var (
		query    string
		ttlQuery string
		params   []any
	)

	var v any
	isBinary := false
	switch x := req.Value.(type) {
	case string:
		if x == "" {
			return errors.New("empty string is not allowed in set operation")
		}
		v = x
	case []uint8:
		isBinary = true
		v = base64.StdEncoding.EncodeToString(x)
	default:
		v = x
	}

	encB, _ := json.Marshal(v)
	enc := string(encB)

	eTagObj, err := uuid.NewRandom()
	if err != nil {
		return fmt.Errorf("failed to generate etag: %w", err)
	}
	eTag := eTagObj.String()

	var (
		result  sql.Result
		maxRows int64 = 1
	)

	ctx, cancel := context.WithTimeout(parentCtx, m.timeout)
	defer cancel()

	if req.Options.Concurrency == state.FirstWrite && (req.ETag == nil || *req.ETag == "") {
		// With first-write-wins and no etag, we can insert the row only if it doesn't exist
		query = `INSERT INTO %[1]s (value, id, eTag, isbinary, expiredate) VALUES (?, ?, ?, ?, %[2]s)`
		params = []any{enc, req.Key, eTag, isBinary}
	} else if req.ETag != nil && *req.ETag != "" {
		// When an eTag is provided do an update - not insert
		query = `UPDATE %[1]s SET value = ?, eTag = ?, isbinary = ?, expiredate = %[2]s
			WHERE id = ? AND eTag = ?`
		params = []any{enc, eTag, isBinary, req.Key, *req.ETag}
	} else {
		// If this is a duplicate MySQL returns that two rows affected
		maxRows = 2
		query = `INSERT INTO %s (value, id, eTag, isbinary, expiredate) VALUES (?, ?, ?, ?, %[2]s) 
			on duplicate key update value=?, eTag=?, isbinary=?, expiredate=%[2]s`
		params = []any{enc, req.Key, eTag, isBinary, enc, eTag, isBinary}
	}

	if ttlSeconds > 0 {
		ttlQuery = "CURRENT_TIMESTAMP + INTERVAL " + strconv.Itoa(ttlSeconds) + " SECOND"
	} else {
		ttlQuery = "NULL"
	}

	result, err = querier.ExecContext(ctx, fmt.Sprintf(query, m.tableName, ttlQuery), params...)

	if err != nil {
		if req.ETag != nil && *req.ETag != "" {
			return state.NewETagError(state.ETagMismatch, err)
		}

		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}

	if rows == 0 {
		err = errors.New(`rows affected error: no rows match given key and eTag`)
		err = state.NewETagError(state.ETagMismatch, err)
		m.logger.Error(err)
		return err
	}

	if rows > maxRows {
		err = fmt.Errorf(`rows affected error: more than %d row affected; actual %d`, maxRows, rows)
		m.logger.Error(err)
		return err
	}

	return nil
}

// BulkSet adds/updates multiple entities on store
// Store Interface.
func (m *MySQL) BulkSet(ctx context.Context, req []state.SetRequest) error {
	m.logger.Debug("Executing BulkSet request")

	tx, err := m.db.Begin()
	if err != nil {
		return err
	}

	if len(req) > 0 {
		for i := range req {
			err = m.setValue(ctx, tx, &req[i])
			if err != nil {
				rollbackErr := tx.Rollback()
				if rollbackErr != nil {
					m.logger.Errorf("Error rolling back transaction: %v", rollbackErr)
				}
				return err
			}
		}
	}

	return tx.Commit()
}

// Multi handles multiple transactions.
// TransactionalStore Interface.
func (m *MySQL) Multi(ctx context.Context, request *state.TransactionalStateRequest) error {
	m.logger.Debug("Executing Multi request")

	tx, err := m.db.Begin()
	if err != nil {
		return err
	}

	for _, req := range request.Operations {
		switch req.Operation {
		case state.Upsert:
			setReq, err := m.getSets(req)
			if err != nil {
				rollbackErr := tx.Rollback()
				if rollbackErr != nil {
					m.logger.Errorf("Error rolling back transaction: %v", rollbackErr)
				}
				return err
			}

			err = m.setValue(ctx, tx, &setReq)
			if err != nil {
				rollbackErr := tx.Rollback()
				if rollbackErr != nil {
					m.logger.Errorf("Error rolling back transaction: %v", rollbackErr)
				}
				return err
			}

		case state.Delete:
			delReq, err := m.getDeletes(req)
			if err != nil {
				rollbackErr := tx.Rollback()
				if rollbackErr != nil {
					m.logger.Errorf("Error rolling back transaction: %v", rollbackErr)
				}
				return err
			}

			err = m.deleteValue(ctx, tx, &delReq)
			if err != nil {
				rollbackErr := tx.Rollback()
				if rollbackErr != nil {
					m.logger.Errorf("Error rolling back transaction: %v", rollbackErr)
				}
				return err
			}

		default:
			return fmt.Errorf("unsupported operation: %s", req.Operation)
		}
	}

	return tx.Commit()
}

// Returns the set requests.
func (m *MySQL) getSets(req state.TransactionalStateOperation) (state.SetRequest, error) {
	setReq, ok := req.Request.(state.SetRequest)
	if !ok {
		return setReq, fmt.Errorf("expecting set request")
	}

	if setReq.Key == "" {
		return setReq, fmt.Errorf("missing key in upsert operation")
	}

	return setReq, nil
}

// Returns the delete requests.
func (m *MySQL) getDeletes(req state.TransactionalStateOperation) (state.DeleteRequest, error) {
	delReq, ok := req.Request.(state.DeleteRequest)
	if !ok {
		return delReq, fmt.Errorf("expecting delete request")
	}

	if delReq.Key == "" {
		return delReq, fmt.Errorf("missing key in delete operation")
	}

	return delReq, nil
}

// BulkGet performs a bulks get operations.
func (m *MySQL) BulkGet(ctx context.Context, req []state.GetRequest) (bool, []state.BulkGetResponse, error) {
	// by default, the store doesn't support bulk get
	// return false so daprd will fallback to call get() method one by one
	return false, nil, nil
}

// Close implements io.Closer.
func (m *MySQL) Close() error {
	if m.db == nil {
		return nil
	}

	err := m.db.Close()
	m.db = nil
	if m.gc != nil {
		return errors.Join(err, m.gc.Close())
	}

	return err
}

// Validates an identifier, such as table or DB name.
// This is based on the rules for allowed unquoted identifiers (https://dev.mysql.com/doc/refman/8.0/en/identifiers.html), but more restrictive as it doesn't allow non-ASCII characters or the $ sign
func validIdentifier(v string) bool {
	if v == "" {
		return false
	}

	// Loop through the string as byte slice as we only care about ASCII characters
	b := []byte(v)
	for i := 0; i < len(b); i++ {
		if (b[i] >= '0' && b[i] <= '9') ||
			(b[i] >= 'a' && b[i] <= 'z') ||
			(b[i] >= 'A' && b[i] <= 'Z') ||
			b[i] == '_' {
			continue
		}
		return false
	}
	return true
}

// Interface for both sql.DB and sql.Tx
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (m *MySQL) GetComponentMetadata() map[string]string {
	metadataStruct := mySQLMetadata{}
	metadataInfo := map[string]string{}
	metadata.GetMetadataInfoFromStructType(reflect.TypeOf(metadataStruct), &metadataInfo)
	return metadataInfo
}
