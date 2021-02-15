CREATE EXTENSION pg_trgm;

CREATE TABLE test_trgm (t text);
CREATE INDEX trgm_idx ON test_trgm USING gin (t gin_trgm_ops);

CREATE TABLE build_artifacts (
    job varchar(256),
    build_id varchar(64),
    files text
);
CREATE UNIQUE INDEX job_build_id_idx ON build_artifacts USING btree (job, build_id);

CREATE TABLE build_statuses (
    job varchar(256),
    build_id varchar(64),
    started_timestamp bigint,
    finished_timestamp bigint,
    result varchar(64)
);
CREATE UNIQUE INDEX build_statuses_result_idx ON build_statuses USING btree (job, build_id, result);

CREATE TABLE test_results (
    job varchar(256),
    build_id varchar(64),
    test varchar(1024),
    finished_timestamp bigint,
    attempt int,
    attempts int,
    status int,
    output text,
    signature text
);
CREATE UNIQUE INDEX job_build_id_test_attempt_idx ON test_results USING btree (job, build_id, test, attempt);
CREATE INDEX gin_idx ON test_results USING gin (job gin_trgm_ops, test gin_trgm_ops, output gin_trgm_ops, (status::text) gin_trgm_ops);
