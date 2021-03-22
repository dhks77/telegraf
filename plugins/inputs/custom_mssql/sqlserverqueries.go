package sqlserver

import (
	_ "github.com/denisenkom/go-mssqldb" // go-mssqldb initialization
)

const sqlServerLogBackupSize string = `
SET DEADLOCK_PRIORITY -10;
DECLARE @TmpTable TABLE (
    size NUMERIC
)
INSERT INTO @TmpTable EXEC sp_MSforeachdb 'USE [?]; SELECT log_space_in_bytes_since_last_backup FROM [?].[sys].[dm_db_log_space_usage]';
SELECT 'sqlserver_expected_log_backup_size' AS [measurement]
	,REPLACE(@@SERVERNAME,'\',':') AS [sql_instance]
	,CAST(SUM(size) AS bigint) AS size
FROM @TmpTable
`
