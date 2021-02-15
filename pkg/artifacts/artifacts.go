package artifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"cloud.google.com/go/storage"
	"github.com/GoogleCloudPlatform/testgrid/metadata/junit"
	"google.golang.org/api/iterator"
	"k8s.io/klog/v2"
)

var ErrNotFound = errors.New("not found")

var junitObject = regexp.MustCompile(`/junit.*\.xml$`)
var buildLogObject = regexp.MustCompile(`/build-log.txt$`)

type Build struct {
	Job       string
	BuildID   string
	GCSBucket string
	GCSPrefix string
}

func (b Build) String() string {
	return fmt.Sprintf("%s @ %s (gs://%s/%s)", b.Job, b.BuildID, b.GCSBucket, b.GCSPrefix)
}

type BuildMeta struct {
	Build *Build              `json:"build"`
	Files map[string]struct{} `json:"files"`
}

type BuildStatus struct {
	StartedTimestamp  int64
	FinishedTimestamp int64
	Result            string
}

type TestStatus int

const (
	TestStatusInfo    = 0
	TestStatusSkipped = 1
	TestStatusError   = 2
	TestStatusFailure = 3
	TestStatusFlake   = 4
	TestStatusSuccess = 5
)

func (s TestStatus) String() string {
	switch s {
	case TestStatusInfo:
		return "Info"
	case TestStatusSkipped:
		return "Skipped"
	case TestStatusError:
		return "Error"
	case TestStatusFailure:
		return "Failure"
	case TestStatusFlake:
		return "Flake"
	case TestStatusSuccess:
		return "Success"
	}
	return fmt.Sprintf("TestStatus(%d)", s)
}

type TestResult struct {
	Test   string
	Status TestStatus
	Output string
}

type Client struct {
	cacheDir  string
	gcsClient *storage.Client
}

func NewClient(gcsClient *storage.Client) *Client {
	return &Client{
		cacheDir:  "./cache",
		gcsClient: gcsClient,
	}
}

func (c *Client) gcsListDir(ctx context.Context, bucket, prefix string) (dirs []string, files []string, err error) {
	bkt := c.gcsClient.Bucket(bucket)
	it := bkt.Objects(ctx, &storage.Query{
		Delimiter: "/",
		Prefix:    prefix,
	})
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if attrs.Prefix == "" {
			files = append(files, attrs.Name)
		} else {
			dirs = append(dirs, attrs.Prefix)
		}
	}
	return dirs, files, nil
}

func (c *Client) gcsListFiles(ctx context.Context, bucket, prefix string) (files []string, err error) {
	bkt := c.gcsClient.Bucket(bucket)
	it := bkt.Objects(ctx, &storage.Query{
		Prefix: prefix,
	})
	for {
		attrs, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return nil, err
		}
		files = append(files, attrs.Name)
	}
	return files, nil
}

func (c *Client) gcsOpen(ctx context.Context, bucket string, object string) (io.ReadCloser, error) {
	// object may have /, so this code won't work on Windows
	path := fmt.Sprintf("%s/%s/%s", c.cacheDir, bucket, object)

	f, err := os.Open(path)
	if err == nil {
		klog.V(4).Infof("Found gs://%s/%s in cache", bucket, object)
		return f, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	klog.V(4).Infof("Downloading gs://%s/%s...", bucket, object)

	err = os.MkdirAll(filepath.Dir(path), os.ModePerm)
	if err != nil {
		return nil, err
	}

	bkt := c.gcsClient.Bucket(bucket)
	r, err := bkt.Object(object).NewReader(ctx)
	if err != nil {
		return nil, fmt.Errorf("unable to open gs://%s/%s: %w", bucket, object, err)
	}
	defer r.Close()

	f, err = os.Create(path + ".part")
	if err != nil {
		return nil, err
	}

	_, err = io.Copy(f, r)
	if err != nil {
		// Best effort cleanup
		_ = f.Close()
		_ = os.Remove(path + ".part")
		return nil, err
	}

	err = f.Close()
	if err != nil {
		return nil, err
	}

	err = os.Rename(path+".part", path)
	if err != nil {
		return nil, err
	}

	return os.Open(path)
}

func (c *Client) FindBuilds(ctx context.Context, name, gcsBucketPrefix string) ([]*Build, error) {
	if !strings.HasSuffix(gcsBucketPrefix, "/") {
		gcsBucketPrefix += "/"
	}
	parts := strings.SplitN(gcsBucketPrefix, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid gcs prefix for %s: %s", name, gcsBucketPrefix)
	}
	bucket, prefix := parts[0], parts[1]

	klog.V(2).Infof("Searching for %s builds (gs://%s/%s)...", name, bucket, prefix)

	var builds []*Build
	dirs, _, err := c.gcsListDir(ctx, bucket, prefix)
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		if len(dir) <= len(prefix)+1 {
			panic(fmt.Errorf("unexpected object from gcs: object is expected to have prefix %q, got %q", prefix, dir))
		}
		buildID := dir[len(prefix) : len(dir)-1]
		build := &Build{
			Job:       name,
			BuildID:   buildID,
			GCSBucket: bucket,
			GCSPrefix: dir,
		}
		builds = append(builds, build)
	}
	return builds, nil
}

func (c *Client) GetBuildMeta(ctx context.Context, build *Build) (*BuildMeta, error) {
	klog.V(4).Infof("Listing gs://%s/%s...", build.GCSBucket, build.GCSPrefix)

	files, err := c.gcsListFiles(ctx, build.GCSBucket, build.GCSPrefix)
	if err != nil {
		return nil, err
	}

	m := make(map[string]struct{})
	for _, f := range files {
		m[f] = struct{}{}
	}

	return &BuildMeta{
		Build: build,
		Files: m,
	}, nil
}

type StartedJson struct {
	Timestamp int64
}

func (c *Client) GetStartedJson(ctx context.Context, build *Build) (StartedJson, error) {
	var j StartedJson
	f, err := c.gcsOpen(ctx, build.GCSBucket, build.GCSPrefix+"started.json")
	if err != nil {
		return j, err
	}
	defer f.Close()
	err = json.NewDecoder(f).Decode(&j)
	return j, err
}

type FinishedJson struct {
	Timestamp int64
	Result    string
}

func (c *Client) GetFinishedJson(ctx context.Context, build *Build) (FinishedJson, error) {
	var j FinishedJson
	f, err := c.gcsOpen(ctx, build.GCSBucket, build.GCSPrefix+"finished.json")
	if err != nil {
		return j, err
	}
	defer f.Close()
	err = json.NewDecoder(f).Decode(&j)
	return j, err
}

func (c *Client) GetBuildStatus(ctx context.Context, buildMeta *BuildMeta) (*BuildStatus, error) {
	if _, ok := buildMeta.Files[buildMeta.Build.GCSPrefix+"finished.json"]; !ok {
		return nil, ErrNotFound
	}

	started, err := c.GetStartedJson(ctx, buildMeta.Build)
	if err != nil {
		return nil, err
	}

	finished, err := c.GetFinishedJson(ctx, buildMeta.Build)
	if err != nil {
		return nil, err
	}

	bs := &BuildStatus{
		StartedTimestamp:  started.Timestamp,
		FinishedTimestamp: finished.Timestamp,
		Result:            finished.Result,
	}
	return bs, nil
}

func analyzeSuite(suite junit.Suite) []*TestResult {
	var results []*TestResult
	for _, result := range suite.Results {
		var output string
		if result.Output != nil {
			output = *result.Output
			if !utf8.ValidString(output) {
				output = fmt.Sprintf("invalid utf8: %s", strings.ToValidUTF8(output, "?"))
			}
		} else {
			output = result.Message(1 << 20) // 1 MiB
		}

		var status TestStatus
		if result.Failure != nil {
			status = TestStatusFailure
		} else if result.Error != nil {
			status = TestStatusError
		} else if result.Skipped != nil {
			status = TestStatusSkipped
		} else {
			status = TestStatusSuccess
		}

		results = append(results, &TestResult{
			Test:   result.Name,
			Status: status,
			Output: output,
		})
	}
	return results
}

func analyzeSuites(suites []junit.Suite) []*TestResult {
	var results []*TestResult
	for _, suite := range suites {
		results = append(results, analyzeSuite(suite)...)
	}
	return results
}

func (c *Client) GetTestResults(ctx context.Context, buildMeta *BuildMeta) ([]*TestResult, error) {
	var results []*TestResult
	for objectName := range buildMeta.Files {
		if junitObject.MatchString(objectName) {
			f, err := c.gcsOpen(ctx, buildMeta.Build.GCSBucket, objectName)
			if err != nil {
				return results, err
			}
			suites, err := junit.ParseStream(f)
			if err != nil {
				return results, fmt.Errorf("unable to parse gs://%s/%s: %w", buildMeta.Build.GCSBucket, objectName, err)
			}
			testResults := analyzeSuites(suites.Suites)
			f.Close()
			results = append(results, testResults...)
		}
	}
	return results, nil
}

func (c *Client) GetBuildLogs(ctx context.Context, buildMeta *BuildMeta) ([]*TestResult, error) {
	var results []*TestResult
	for objectName := range buildMeta.Files {
		if buildLogObject.MatchString(objectName) {
			f, err := c.gcsOpen(ctx, buildMeta.Build.GCSBucket, objectName)
			if err != nil {
				return results, err
			}
			content, err := ioutil.ReadAll(f)
			if err != nil {
				return results, fmt.Errorf("unable to read gs://%s/%s: %w", buildMeta.Build.GCSBucket, objectName, err)
			}
			f.Close()
			if !utf8.Valid(content) {
				content = bytes.ToValidUTF8(content, []byte("?"))
			}
			content = bytes.ReplaceAll(content, []byte("\x00"), []byte("?"))
			results = append(results, &TestResult{
				Test:   objectName[len(buildMeta.Build.GCSPrefix):],
				Status: TestStatusInfo,
				Output: string(content),
			})
		}
	}
	return results, nil
}
