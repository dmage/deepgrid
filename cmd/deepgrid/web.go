package main

import (
	"context"
	"html/template"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v4"
	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/spf13/cobra"
	"k8s.io/klog/v2"
)

type pgxLogger struct {
}

func (l pgxLogger) Log(ctx context.Context, level pgx.LogLevel, msg string, data map[string]interface{}) {
	klog.Infof("SQL: %q %q", msg, data)
}

func init() {
	rootCmd.AddCommand(webCmd)
}

type ColumnInfo struct {
	Title string
	Field string
	Query string
}

var templateFuncs = template.FuncMap{
	"reescaper": func(s string) string {
		return regexp.QuoteMeta(s)
	},
}

var webCmd = &cobra.Command{
	Use:   "web",
	Short: "Print the version number of Hugo",
	Long:  `All software has versions. This is Hugo's`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		t := template.Must(template.New("").Funcs(templateFuncs).ParseGlob("./templates/*.html"))

		poolConfig, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
		if err != nil {
			klog.Exitf("Unable to parse DATABASE_URL: %s", err)
		}

		poolConfig.ConnConfig.Logger = &pgxLogger{}

		pool, err := pgxpool.ConnectConfig(ctx, poolConfig)
		if err != nil {
			klog.Exitf("Unable to connect to database: %s", err)
		}
		defer pool.Close()

		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			ctx := r.Context()
			conn, err := pool.Acquire(ctx)
			if err != nil {
				klog.Errorf("%s", err)
				return
			}
			defer conn.Release()

			startTime := time.Now()

			columnsRaw := r.URL.Query().Get("columns")
			var columns []*ColumnInfo
			if columnsRaw != "" {
				for _, col := range strings.Split(columnsRaw, ",") {
					switch col {
					case "job":
						columns = append(columns, &ColumnInfo{
							Title: "Job",
							Field: "Job",
							Query: col,
						})
					case "build_id":
						columns = append(columns, &ColumnInfo{
							Title: "Build ID",
							Field: "BuildID",
							Query: col,
						})
					case "test":
						columns = append(columns, &ColumnInfo{
							Title: "Test",
							Field: "Test",
							Query: col,
						})
					case "signature":
						columns = append(columns, &ColumnInfo{
							Title: "Signature",
							Field: "Signature",
							Query: col,
						})
					default:
						panic("FIXME")
					}
				}
			}

			job := r.URL.Query().Get("job")
			test := r.URL.Query().Get("test")
			output := r.URL.Query().Get("output")
			signature := strings.ReplaceAll(r.URL.Query().Get("signature"), "\x0d", "")
			count := r.URL.Query().Get("count")
			order := r.URL.Query().Get("order")
			age := r.URL.Query().Get("age")

			finishedAfter := int64(0)
			if age != "" {
				i, err := strconv.Atoi(age)
				if err != nil {
					panic(err)
				}
				finishedAfter = time.Now().Unix() - int64(i)
			}

			var groupByFields []string
			var sqlSelect []string
			for _, col := range columns {
				sqlSelect = append(sqlSelect, "tr."+col.Query)
				groupByFields = append(groupByFields, "tr."+col.Query)
			}
			sqlJoin := ""
			sqlOrderBy := ""
			if count == "tests" {
				sqlSelect = append(
					sqlSelect,
					`COUNT(*)`,
					`COUNT(*) FILTER (WHERE status = 3) AS failures`,
					`COUNT(*) FILTER (WHERE status = 4) AS flakes`,
					`COUNT(*) FILTER (WHERE status = 5) AS successes`,
					`COUNT(*) FILTER (WHERE status = 3 AND output ~ $3) AS failures_matches`,
					`COUNT(*) FILTER (WHERE status = 4 AND output ~ $3) AS flakes_matches`,
					`COUNT(*) FILTER (WHERE status = 5 AND output ~ $3) AS successes_matches`,
					`COUNT(DISTINCT tr.signature) FILTER (WHERE status = 3 OR status = 4) AS signatures`,
				)
				sqlOrderBy = "failures DESC, flakes DESC, successes DESC"
			} else {
				sqlCount := "DISTINCT CONCAT(tr.job, '/', tr.build_id)"
				sqlSelect = append(
					sqlSelect,
					`COUNT(`+sqlCount+`)`,
					`COUNT(`+sqlCount+`) FILTER (WHERE bs.result = 'FAILURE') AS failures`,
					`COUNT(`+sqlCount+`) FILTER (WHERE bs.result = 'SUCCESS') AS successes`,
					`COUNT(`+sqlCount+`) FILTER (WHERE output ~ $3)`,
				)
				sqlJoin = "JOIN build_statuses bs ON bs.job = tr.job AND bs.build_id = tr.build_id"
				sqlOrderBy = "failures DESC, successes DESC"
			}
			sqlGroupBy := ""
			if len(groupByFields) > 0 {
				sqlGroupBy = "GROUP BY " + strings.Join(groupByFields, ", ") + " HAVING COUNT(*) FILTER (WHERE output ~ $3) > 0"
			}
			if order == "timestamp" {
				sqlOrderBy = "MAX(finished_timestamp) DESC"
			}

			rows, err := conn.Query(ctx, `
				SELECT `+strings.Join(sqlSelect, ",")+`
				FROM test_results tr
				`+sqlJoin+`
				WHERE tr.job ~ $1 AND tr.test ~ $2 AND tr.signature ~ $4 AND tr.finished_timestamp > $5
				`+sqlGroupBy+`
				ORDER BY `+sqlOrderBy+`
				LIMIT 50
			`, job, test, output, signature, finishedAfter)
			if err != nil {
				klog.Errorf("%s", err)
				return
			}
			defer rows.Close()

			var data = []map[string]interface{}{}
			for rows.Next() {
				var job, buildID, test, signature string
				var total, failures, flakes, successes, failuresMatches, flakesMatches, successesMatches, signatures, matches int
				var dest []interface{}
				for _, col := range columns {
					switch col.Query {
					case "job":
						dest = append(dest, &job)
					case "build_id":
						dest = append(dest, &buildID)
					case "test":
						dest = append(dest, &test)
					case "signature":
						dest = append(dest, &signature)
					default:
						panic("FIXME")
					}
				}
				if count == "tests" {
					dest = append(dest, &total, &failures, &flakes, &successes, &failuresMatches, &flakesMatches, &successesMatches, &signatures)
				} else {
					dest = append(dest, &total, &failures, &successes, &matches)
				}
				err = rows.Scan(dest...)
				if err != nil {
					klog.Errorf("%s", err)
					return
				}
				d := map[string]interface{}{
					"Job":       job,
					"BuildID":   buildID,
					"Test":      test,
					"Signature": signature,
				}
				if count == "tests" {
					d["Total"] = total
					d["Failures"] = failures
					d["Flakes"] = flakes
					d["Successes"] = successes
					d["FailuresMatches"] = failuresMatches
					d["FlakesMatches"] = flakesMatches
					d["SuccessesMatches"] = successesMatches
					d["Signatures"] = signatures
				} else {
					d["Total"] = total
					d["Failures"] = failures
					d["Successes"] = successes
					d["Matches"] = matches
				}
				data = append(data, d)
			}
			if rows.Err() != nil {
				klog.Errorf("%s", rows.Err())
				return
			}

			endTime := time.Now()

			err = t.ExecuteTemplate(w, "index.html", map[string]interface{}{
				"Query": map[string]string{
					"Columns":   columnsRaw,
					"Job":       job,
					"Test":      test,
					"Output":    output,
					"Signature": signature,
					"Count":     count,
					"Order":     order,
					"Age":       age,
				},
				"Columns":  columns,
				"Data":     data,
				"Duration": endTime.Sub(startTime),
			})
			if err != nil {
				klog.Errorf("%s", err)
			}
		})

		klog.Info("Listening on http://localhost:8080")
		log.Fatal(http.ListenAndServe(":8080", nil))
	},
}
