package main

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"sort"
	"strings"

	"cloud.google.com/go/storage"
	"github.com/dmage/deepgrid/pkg/artifacts"
	"github.com/dmage/deepgrid/pkg/config"
	"github.com/dmage/deepgrid/pkg/denoise"
	"github.com/jackc/pgx/v4"
	"github.com/spf13/cobra"
	"google.golang.org/api/option"
	"k8s.io/klog/v2"
)

func loadBuildMeta(ctx context.Context, conn *pgx.Conn, build *artifacts.Build) (*artifacts.BuildMeta, error) {
	var filesBuf []byte
	err := conn.QueryRow(ctx, "select files from build_artifacts where job=$1 and build_id=$2", build.Job, build.BuildID).Scan(&filesBuf)
	if err != nil {
		return nil, err
	}

	meta := &artifacts.BuildMeta{
		Build: build,
	}
	err = json.Unmarshal(filesBuf, &meta.Files)
	return meta, err
}

func saveBuildMeta(ctx context.Context, conn *pgx.Conn, buildMeta *artifacts.BuildMeta) error {
	filesBuf, err := json.Marshal(buildMeta.Files)
	if err != nil {
		return err
	}

	_, err = conn.Exec(ctx, "insert into build_artifacts (job, build_id, files) values ($1, $2, $3)", buildMeta.Build.Job, buildMeta.Build.BuildID, filesBuf)
	return err
}

type DBBuildStatus struct {
	Job               string
	BuildID           string
	FinishedTimestamp int64
	Result            string
}

func saveBuildStatus(ctx context.Context, conn *pgx.Conn, build *artifacts.Build, status *artifacts.BuildStatus) error {
	_, err := conn.Exec(
		ctx, "insert into build_statuses (job, build_id, started_timestamp, finished_timestamp, result) values ($1, $2, $3, $4, $5)",
		build.Job, build.BuildID, status.StartedTimestamp, status.FinishedTimestamp, status.Result,
	)
	return err
}

func loadBuildStatus(ctx context.Context, conn *pgx.Conn, build *artifacts.Build) (*artifacts.BuildStatus, error) {
	status := &artifacts.BuildStatus{}
	err := conn.QueryRow(
		ctx,
		"select started_timestamp, finished_timestamp, result from build_statuses where job=$1 and build_id=$2",
		build.Job, build.BuildID,
	).Scan(&status.StartedTimestamp, &status.FinishedTimestamp, &status.Result)
	if err != nil {
		return nil, err
	}

	klog.V(4).Infof("Loaded build status for %s @ %s from cache", build.Job, build.BuildID)

	return status, nil
}

type DBTestResult struct {
	Job               string
	BuildID           string
	Test              string
	FinishedTimestamp int64
	Attempt           int
	Attempts          int
	Status            int
	Output            string
	Signature         string
}

func saveTestResult(ctx context.Context, conn *pgx.Conn, result *DBTestResult) error {
	klog.V(5).Infof("Saving %s @ %s: %s...", result.Job, result.BuildID, result.Test)

	_, err := conn.Exec(
		ctx,
		"insert into test_results (job, build_id, test, finished_timestamp, attempt, attempts, status, output, signature) values ($1, $2, $3, $4, $5, $6, $7, $8, $9) on conflict do nothing",
		result.Job,
		result.BuildID,
		result.Test,
		result.FinishedTimestamp,
		result.Attempt,
		result.Attempts,
		result.Status,
		result.Output,
		result.Signature,
	)
	return err
}

var (
	excludeLineRe = regexp.MustCompile(`(?i)(?:INFO: .* event for)`)
	errorLineRe   = regexp.MustCompile(`(?i)(?:error|fail|unable|illegal|violation|forbidden|cannot|can't|should not|did not|didn't|isn't|is not|aren't|are not|timed?.?out|unavailable)`)
)

func generateSignature(data string) string {
	errorLines := map[string]bool{}
	for _, line := range strings.Split(data, "\n") {
		line = denoise.Denoise(line)
		if errorLineRe.MatchString(line) && !excludeLineRe.MatchString(line) {
			errorLines[line] = true
		}
	}

	var lines []string
	for line := range errorLines {
		lines = append(lines, line)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

func init() {
	rootCmd.AddCommand(indexCmd)
}

var indexCmd = &cobra.Command{
	Use:   "index",
	Short: "Print the version number of Hugo",
	Long:  `All software has versions. This is Hugo's`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		conn, err := pgx.Connect(ctx, os.Getenv("DATABASE_URL"))
		if err != nil {
			klog.Exitf("Unable to connect to database: %s", err)
		}
		defer conn.Close(ctx)

		cfg, err := config.LoadFromFile("./config.yaml")
		if err != nil {
			klog.Fatal(err)
		}

		gcsClient, err := storage.NewClient(ctx, option.WithoutAuthentication())
		if err != nil {
			klog.Fatal(err)
		}

		client := artifacts.NewClient(gcsClient)

		for _, testGroup := range cfg.TestGroups {
			builds, err := client.FindBuilds(ctx, testGroup.Name, testGroup.GCSPrefix)
			if err != nil {
				klog.Fatal(err)
			}

			const maxBuilds = 5
			if len(builds) > maxBuilds {
				builds = builds[len(builds)-maxBuilds:]
			}

			for _, build := range builds {
				status, err := loadBuildStatus(ctx, conn, build)
				if err == nil {
					continue
				} else if err != pgx.ErrNoRows {
					klog.Fatal(err)
				}

				buildMetaCached := true
				buildMeta, err := loadBuildMeta(ctx, conn, build)
				if err == pgx.ErrNoRows {
					buildMeta, err = client.GetBuildMeta(ctx, build)
					if err != nil {
						klog.Fatal(err)
					}
					buildMetaCached = false
				} else if err != nil {
					klog.Fatal(err)
				}

				status, err = client.GetBuildStatus(ctx, buildMeta)
				if err == artifacts.ErrNotFound {
					continue
				} else if err != nil {
					klog.Fatal(err)
				}

				if !buildMetaCached {
					err = saveBuildMeta(ctx, conn, buildMeta)
					if err != nil {
						klog.Fatal(err)
					}
				}

				//if status.FinishedTimestamp < time.Now().Unix()-24*60*60 {
				//	continue
				//}

				resultsList, err := client.GetTestResults(ctx, buildMeta)
				if err != nil {
					klog.Fatal(err)
				}

				buildLogs, err := client.GetBuildLogs(ctx, buildMeta)
				if err != nil {
					klog.Fatal(err)
				}

				resultsList = append(resultsList, buildLogs...)

				results := map[string][]*artifacts.TestResult{}
				for _, result := range resultsList {
					if result.Status == artifacts.TestStatusSuccess {
						for _, prev := range results[result.Test] {
							if prev.Status == artifacts.TestStatusFailure {
								prev.Status = artifacts.TestStatusFlake
							}
						}
					}
					results[result.Test] = append(results[result.Test], result)
				}

				for test, testResults := range results {
					for i, r := range testResults {
						dbTestResult := &DBTestResult{
							Job:               build.Job,
							BuildID:           build.BuildID,
							Test:              test,
							FinishedTimestamp: status.FinishedTimestamp,
							Attempt:           i - len(testResults) + 1,
							Attempts:          len(testResults),
							Status:            int(r.Status),
							Output:            r.Output,
							Signature:         generateSignature(r.Output),
						}
						err = saveTestResult(ctx, conn, dbTestResult)
						if err != nil {
							klog.Fatal(err)
						}
					}
				}

				err = saveBuildStatus(ctx, conn, build, status)
				if err != nil {
					klog.Fatal(err)
				}
			}
		}
	},
}
