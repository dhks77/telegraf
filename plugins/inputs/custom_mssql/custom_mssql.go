package sqlserver

import (
	"database/sql"
	"fmt"
	"log"
	"sync"
	"time"

	_ "github.com/denisenkom/go-mssqldb" // go-mssqldb initialization
	"github.com/influxdata/telegraf"
	"github.com/influxdata/telegraf/plugins/inputs"
)

// SQLServer struct
type SQLServer struct {
	Servers       []string `toml:"servers"`
	HealthMetric  bool     `toml:"health_metric"`
	queries       MapQuery
	isInitialized bool
}

// Query struct
type Query struct {
	ScriptName     string
	Script         string
	ResultByRow    bool
	OrderedColumns []string
}

// MapQuery type
type MapQuery map[string]Query

// HealthMetric struct tracking the number of attempted vs successful connections for each connection string
type HealthMetric struct {
	AttemptedQueries  int
	SuccessfulQueries int
}

const defaultServer = "Server=.;app name=telegraf;log=1;"

const (
	healthMetricName              = "sqlserver_telegraf_health"
	healthMetricInstanceTag       = "sql_instance"
	healthMetricDatabaseTag       = "database_name"
	healthMetricAttemptedQueries  = "attempted_queries"
	healthMetricSuccessfulQueries = "successful_queries"
)

const sampleConfig = `
## Specify instances to monitor with a list of connection strings.
## All connection parameters are optional.
## By default, the host is localhost, listening on default port, TCP 1433.
##   for Windows, the user is the currently running AD user (SSO).
##   See https://github.com/denisenkom/go-mssqldb for detailed connection
##   parameters, in particular, tls connections can be created like so:
##   "encrypt=true;certificate=<cert>;hostNameInCertificate=<SqlServer host fqdn>"
servers = [
  "Server=192.168.1.10;Port=1433;User Id=<user>;Password=<pw>;app name=telegraf;log=1;",
]

## "database_type" enables a specific set of queries depending on the database type. If specified, it replaces azuredb = true/false and query_version = 2
## In the config file, the sql server plugin section should be repeated each with a set of servers for a specific database_type.
## Possible values for database_type are - "AzureSQLDB" or "AzureSQLManagedInstance" or "SQLServer"

## Queries enabled by default for database_type = "AzureSQLDB" are - 
## AzureSQLDBResourceStats, AzureSQLDBResourceGovernance, AzureSQLDBWaitStats, AzureSQLDBDatabaseIO, AzureSQLDBServerProperties, 
## AzureSQLDBOsWaitstats, AzureSQLDBMemoryClerks, AzureSQLDBPerformanceCounters, AzureSQLDBRequests, AzureSQLDBSchedulers

# database_type = "AzureSQLDB"

## A list of queries to include. If not specified, all the above listed queries are used.
# include_query = []

## A list of queries to explicitly ignore.
# exclude_query = []

## Queries enabled by default for database_type = "AzureSQLManagedInstance" are - 
## AzureSQLMIResourceStats, AzureSQLMIResourceGovernance, AzureSQLMIDatabaseIO, AzureSQLMIServerProperties, AzureSQLMIOsWaitstats, 
## AzureSQLMIMemoryClerks, AzureSQLMIPerformanceCounters, AzureSQLMIRequests, AzureSQLMISchedulers

# database_type = "AzureSQLManagedInstance"

# include_query = []

# exclude_query = []

## Queries enabled by default for database_type = "SQLServer" are - 
## SQLServerPerformanceCounters, SQLServerWaitStatsCategorized, SQLServerDatabaseIO, SQLServerProperties, SQLServerMemoryClerks, 
## SQLServerSchedulers, SQLServerRequests, SQLServerVolumeSpace, SQLServerCpu

database_type = "SQLServer"

include_query = []

## SQLServerAvailabilityReplicaStates and SQLServerDatabaseReplicaStates are optional queries and hence excluded here as default
exclude_query = ["SQLServerAvailabilityReplicaStates", "SQLServerDatabaseReplicaStates"]
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

func initQueries(s *SQLServer) error {
	s.queries = make(MapQuery)
	queries := s.queries
	log.Printf("I! [inputs.custom_mssql]")

	queries["SQLServerLogBackupSize"] = Query{ScriptName: "SQLServerLogBackupSize", Script: sqlServerLogBackupSize, ResultByRow: false}
	s.isInitialized = true

	return nil
}

// Gather collect data from SQL Server
func (s *SQLServer) Gather(acc telegraf.Accumulator) error {
	if !s.isInitialized {
		if err := initQueries(s); err != nil {
			acc.AddError(err)
			return err
		}
	}

	var wg sync.WaitGroup
	var mutex sync.Mutex
	var healthMetrics = make(map[string]*HealthMetric)

	for _, serv := range s.Servers {
		for _, query := range s.queries {
			wg.Add(1)
			go func(serv string, query Query) {
				defer wg.Done()
				queryError := s.gatherServer(serv, query, acc)

				if s.HealthMetric {
					mutex.Lock()
					s.gatherHealth(healthMetrics, serv, queryError)
					mutex.Unlock()
				}

				acc.AddError(queryError)
			}(serv, query)
		}
	}

	wg.Wait()

	if s.HealthMetric {
		s.accHealth(healthMetrics, acc)
	}

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
				fields[header] = (*val)
			}
		}
		// add fields to Accumulator
		acc.AddFields(measurement, fields, tags, time.Now())
	}
	return nil
}

// gatherHealth stores info about any query errors in the healthMetrics map
func (s *SQLServer) gatherHealth(healthMetrics map[string]*HealthMetric, serv string, queryError error) {
	if healthMetrics[serv] == nil {
		healthMetrics[serv] = &HealthMetric{}
	}

	healthMetrics[serv].AttemptedQueries++
	if queryError == nil {
		healthMetrics[serv].SuccessfulQueries++
	}
}

// accHealth accumulates the query health data contained within the healthMetrics map
func (s *SQLServer) accHealth(healthMetrics map[string]*HealthMetric, acc telegraf.Accumulator) {
	for connectionString, connectionStats := range healthMetrics {
		sqlInstance, databaseName := getConnectionIdentifiers(connectionString)
		tags := map[string]string{healthMetricInstanceTag: sqlInstance, healthMetricDatabaseTag: databaseName}
		fields := map[string]interface{}{
			healthMetricAttemptedQueries:  connectionStats.AttemptedQueries,
			healthMetricSuccessfulQueries: connectionStats.SuccessfulQueries,
		}

		acc.AddFields(healthMetricName, fields, tags, time.Now())
	}
}

func (s *SQLServer) Init() error {
	if len(s.Servers) == 0 {
		log.Println("W! Warning: Server list is empty.")
	}

	return nil
}

func init() {
	inputs.Add("sqlserver", func() telegraf.Input {
		return &SQLServer{Servers: []string{defaultServer}}
	})
}
