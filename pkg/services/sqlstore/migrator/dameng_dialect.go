package migrator

import (
	"errors"
	"fmt"
	"gitee.com/travelliu/dm"
	"strconv"
	"strings"
	"xorm.io/xorm/core"

	_ "gitee.com/travelliu/dm"
	"github.com/grafana/grafana/pkg/util/errutil"
	"xorm.io/xorm"
)

type DmDialect struct {
	BaseDialect
}

func (db *DmDialect) IsDeadlock(err error) bool {
	return false
}

func NewDmDialect(engine *xorm.Engine) Dialect {
	d := DmDialect{}
	d.BaseDialect.dialect = &d
	d.BaseDialect.engine = engine
	d.BaseDialect.driverName = DM
	return &d
}

func (db *DmDialect) SupportEngine() bool {
	return false
}

func (db *DmDialect) Quote(name string) string {
	return `"` + name + `"`
}

func (db *DmDialect) AutoIncrStr() string {
	return "IDENTITY"
}

func (db *DmDialect) BooleanStr(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func (b *DmDialect) ColString(col *Column) string {
	sql := b.dialect.Quote(col.Name) + " "

	sql += b.dialect.SQLType(col) + " "

	if col.IsPrimaryKey {
		sql += "PRIMARY KEY "
		if col.IsAutoIncrement {
			sql += b.dialect.AutoIncrStr() + " "
		}
	}

	if b.dialect.ShowCreateNull() && !col.IsPrimaryKey {
		if col.Nullable {
			sql += "NULL "
		} else {
			sql += "NOT NULL "
		}
	}

	if col.Default != "" {
		sql += "DEFAULT " + b.dialect.Default(col) + " "
	}

	return sql
}

func (db *DmDialect) SQLType(c *Column) string {
	var res string
	switch t := c.Type; t {
	case core.TinyInt, "BYTE":
		return "TINYINT"
	case core.SmallInt, core.MediumInt, core.Int, core.Integer, core.UnsignedTinyInt:
		return "INTEGER"
	case core.BigInt,
		core.UnsignedBigInt, core.UnsignedBit, core.UnsignedInt,
		core.Serial, core.BigSerial:
		return "BIGINT"
	case core.Bit, core.Bool, core.Boolean:
		return core.Bit
	case core.Uuid:
		res = core.Varchar
		c.Length = 40
	case core.Binary:
		if c.Length == 0 {
			return core.Binary + "(MAX)"
		}
	case core.VarBinary, core.Blob, core.TinyBlob, core.MediumBlob, core.LongBlob, core.Bytea:
		return core.VarBinary
	case core.Date:
		return core.Date
	case core.Time:
		if c.Length > 0 {
			return fmt.Sprintf("%s(%d)", core.Time, c.Length)
		}
		return core.Time
	case core.DateTime, core.TimeStamp:
		res = core.TimeStamp
	case core.TimeStampz:
		if c.Length > 0 {
			return fmt.Sprintf("TIMESTAMP(%d) WITH TIME ZONE", c.Length)
		}
		return "TIMESTAMP WITH TIME ZONE"
	case core.Float:
		res = "FLOAT"
	case core.Real, core.Double:
		res = "REAL"
	case core.Numeric, core.Decimal, "NUMBER":
		res = "NUMERIC"
	case core.Text, core.Json:
		return "TEXT"
	case core.MediumText, core.LongText:
		res = "CLOB"
	case core.Char, core.Varchar, core.TinyText:
		res = "VARCHAR2"
	default:
		res = t
	}

	hasLen1 := c.Length > 0
	hasLen2 := c.Length2 > 0

	if hasLen2 {
		res += "(" + strconv.Itoa(c.Length) + "," + strconv.Itoa(c.Length2) + ")"
	} else if hasLen1 {
		res += "(" + strconv.Itoa(c.Length) + ")"
	}
	return res
}

func (db *DmDialect) isThisError(err error, errcode string) bool {
	var driverErr *dm.DmError
	if errors.As(err, &driverErr) {
		if string(driverErr.ErrCode) == errcode {
			return true
		}
	}

	return false
}

func (db *DmDialect) IsUniqueConstraintViolation(err error) bool {
	return db.isThisError(err, "23505")
}

func (db *DmDialect) ErrorMessage(err error) string {
	var driverErr dm.DmError
	if errors.As(err, &driverErr) {
		return driverErr.ErrText
	}
	return ""
}

func (db *DmDialect) UpdateTableSQL(tableName string, columns []*Column) string {
	var statements []string
	for _, col := range columns {
		statements = append(statements, col.StringNoPk(db))
	}
	return fmt.Sprintf("alter table %s.%s modify (%s);",
		db.Quote(db.engine.Dialect().URI().Schema),
		db.Quote(tableName),
		strings.Join(statements, ", "),
	)
}

func (db *DmDialect) IndexCheckSQL(tableName, indexName string) (string, []interface{}) {
	args := []interface{}{tableName, indexName, db.engine.Dialect().URI().Schema}
	sql := `SELECT INDEX_NAME FROM ALL_INDEXES WHERE TABLE_NAME = ? AND INDEX_NAME = ? AND OWNER = ?`
	return sql, args
}

func (db *DmDialect) ColumnCheckSQL(tableName, columnName string) (string, []interface{}) {
	args := []interface{}{tableName, columnName, db.engine.Dialect().URI().Schema}
	sql := "SELECT 1 FROM ALL_INDEXES WHERE TABLE_NAME = ? AND INDEX_NAME = ? AND OWNER = ?"
	return sql, args
}

func (db *DmDialect) CleanDB() error {
	tables, err := db.engine.DBMetas()
	if err != nil {
		return err
	}
	sess := db.engine.NewSession()
	defer sess.Close()

	for _, table := range tables {
		switch table.Name {
		default:
			if _, err := sess.Exec(fmt.Sprintf(`drop table "%s";`, table.Name)); err != nil {
				return errutil.Wrapf(err, "failed to delete table %q", table.Name)
			}
		}
	}

	return nil
}

// TruncateDBTables truncates all the tables.
// A special case is the dashboard_acl table where we keep the default permissions.
func (db *DmDialect) TruncateDBTables() error {
	tables, err := db.engine.DBMetas()
	if err != nil {
		return err
	}
	sess := db.engine.NewSession()
	defer sess.Close()

	for _, table := range tables {
		switch table.Name {
		case "dashboard_acl":
			// keep default dashboard permissions
			if _, err := sess.Exec(fmt.Sprintf("DELETE FROM %v WHERE dashboard_id != -1 AND org_id != -1;", db.Quote(table.Name))); err != nil {
				return errutil.Wrapf(err, "failed to truncate table %q", table.Name)
			}
			if _, err := sess.Exec(fmt.Sprintf("ALTER TABLE %v AUTO_INCREMENT = 3;", db.Quote(table.Name))); err != nil {
				return errutil.Wrapf(err, "failed to reset table %q", table.Name)
			}
		default:
			if _, err := sess.Exec(fmt.Sprintf("TRUNCATE TABLE %v;", db.Quote(table.Name))); err != nil {
				return errutil.Wrapf(err, "failed to truncate table %q", table.Name)
			}
		}
	}

	return nil
}

// UpsertSQL returns the upsert sql statement for dameng dialect
func (db *DmDialect) UpsertSQL(tableName string, keyCols, updateCols []string) string {
	columnsStr := strings.Builder{}
	colPlaceHoldersStr := strings.Builder{}
	setStr := strings.Builder{}

	separator := ", "
	for i, c := range updateCols {
		if i == len(updateCols)-1 {
			separator = ""
		}
		columnsStr.WriteString(fmt.Sprintf("%s%s", db.Quote(c), separator))
		colPlaceHoldersStr.WriteString(fmt.Sprintf("?%s", separator))
		setStr.WriteString(fmt.Sprintf("%s=VALUES(%s)%s", db.Quote(c), db.Quote(c), separator))
	}

	s := fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s) ON DUPLICATE KEY UPDATE %s`,
		tableName,
		columnsStr.String(),
		colPlaceHoldersStr.String(),
		setStr.String(),
	)
	return s
}

func (b *DmDialect) DropIndexSQL(tableName string, index *Index) string {
	quote := b.dialect.Quote
	name := index.XName(tableName)
	return fmt.Sprintf("DROP INDEX %s.%s", quote(b.engine.Dialect().URI().Schema), quote(name))
}

func (b *DmDialect) CreateIndexSQL(tableName string, index *Index) string {
	quote := b.dialect.Quote
	var unique string
	if index.Type == UniqueIndex {
		unique = " UNIQUE"
	}

	idxName := index.XName(tableName)

	quotedCols := []string{}
	for _, col := range index.Cols {
		quotedCols = append(quotedCols, b.dialect.Quote(col))
	}

	return fmt.Sprintf("CREATE%s INDEX %v ON %s.%v (%v);", unique, quote(idxName), quote(b.engine.Dialect().URI().Schema), quote(tableName), strings.Join(quotedCols, ","))
}
