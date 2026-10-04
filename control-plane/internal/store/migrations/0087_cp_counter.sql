-- A counter shared by every Control Plane task (issue #1601). An ecs-ec2 workspace's Start
-- count was kept in each CP process, so a release on one CP could not see a Start made on
-- another and pulled the home from under it. name is what is counted, value only ever grows.
-- NOTE the migrator splits on the semicolon, so comments must not contain one.
CREATE TABLE IF NOT EXISTS cp_counter (
    name  TEXT PRIMARY KEY,
    value INTEGER NOT NULL
)
