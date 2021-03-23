package sqlserver

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/denisenkom/go-mssqldb" // go-mssqldb initialization
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
)

// SQLServer struct
type SQLServer struct {
	Servers []string `toml:"servers"`
}

// Query struct
type Query struct {
	ScriptName     string
	Script         string
	ResultByRow    bool
	OrderedColumns []string
}

const defaultServer = "Server=.;app name=telegraf;log=1;"

const sampleConfig = `
servers = [
  "Server=192.168.1.10;Port=1433;User Id=<user>;Password=<pw>;app name=telegraf;log=1;",
]
`

// SampleConfig return the sample configuration
func (s *SQLServer) SampleConfig() string {
	return sampleConfig
}

// Description return plugin description
func (s *SQLServer) Description() string {
	return "Read metrics for custom mssql metric"
}

type scanner interface {
	Scan(dest ...interface{}) error
}

// Gather collect data from SQL Server
func (s *SQLServer) Gather(acc telegraf.Accumulator) error {
	//var wg sync.WaitGroup
	//query := Query{ScriptName: "SQLServerLogBackupSize-CustomMssql", Script: sqlServerLogBackupSize, ResultByRow: false}
	//
	//for _, server := range s.Servers {
	//	wg.Add(1)
	//	go func(serv string, query Query) {
	//		defer wg.Done()
	//		queryError := s.gatherServer(serv, query, acc)
	//		acc.AddError(queryError)
	//	}(server, query)
	//}
	//
	//wg.Wait()
	var fields = make(map[string]interface{})
	var tags = make(map[string]string)
	fields["value"] = 123123
	acc.AddCounter("1", fields, tags, time.Now())

	return nil
}

func (s *SQLServer) gatherServer(server string, query Query, acc telegraf.Accumulator) error {
	// deferred opening
	conn, err := sql.Open("mssql", server)
	if err != nil {
		return err
	}
	defer conn.Close()

	// execute query
	rows, err := conn.Query(query.Script)
	if err != nil {
		return fmt.Errorf("Script %s failed: %w", query.ScriptName, err)
		//return   err
	}
	defer rows.Close()

	// grab the column information from the result
	query.OrderedColumns, err = rows.Columns()
	if err != nil {
		return err
	}

	for rows.Next() {
		err = s.accRow(query, acc, rows)
		if err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *SQLServer) accRow(query Query, acc telegraf.Accumulator, row scanner) error {
	var columnVars []interface{}
	var fields = make(map[string]interface{})

	// store the column name with its *interface{}
	columnMap := make(map[string]*interface{})
	for _, column := range query.OrderedColumns {
		columnMap[column] = new(interface{})
	}
	// populate the array of interface{} with the pointers in the right order
	for i := 0; i < len(columnMap); i++ {
		columnVars = append(columnVars, columnMap[query.OrderedColumns[i]])
	}
	// deconstruct array of variables and send to Scan
	err := row.Scan(columnVars...)
	if err != nil {
		return err
	}

	// measurement: identified by the header
	// tags: all other fields of type string
	tags := map[string]string{}
	var measurement string
	for header, val := range columnMap {
		if str, ok := (*val).(string); ok {
			if header == "measurement" {
				measurement = str
			} else {
				tags[header] = str
			}
		}
	}

	if query.ResultByRow {
		// add measurement to Accumulator
		acc.AddFields(measurement,
			map[string]interface{}{"value": *columnMap["value"]},
			tags, time.Now())
	} else {
		// values
		for header, val := range columnMap {
			if _, ok := (*val).(string); !ok {
				fields[header] = *val
			}
		}
		// add fields to Accumulator
		acc.AddFields(measurement, fields, tags, time.Now())
	}
	return nil
}

func (s *SQLServer) Init() error {
	if len(s.Servers) == 0 {
		log.Println("W! Warning: Server list is empty.")
	}

	return nil
}

func init() {
	inputs.Add("custom_mssql", func() telegraf.Input {
		return &SQLServer{Servers: []string{defaultServer}}
	})
}
