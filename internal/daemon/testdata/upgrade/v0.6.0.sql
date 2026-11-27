-- A store as a v0.6.0 daemon left it when it stopped, with a run leased and live: the upgrade
-- fixture for TestUpgradeFromV060 (LUCY-OPS-005). Made by v0.6.0's own code (git archive v0.6.0),
-- on a clock fixed at 2026-11-27T12:00:00Z: daemon.Started; runner.Broker Submit of f1#1 and f2#1
-- for org acme; Lease of f1#1 by runner acme-host-1 (fence 4, 60 s); a Heartbeat at +20 s
-- streaming two events; daemon.Stopped at +30 s. Then dumped with `sqlite3 lucy.db .dump`.
-- Do not edit by hand: it stands for what a real v0.6.0 install holds.
PRAGMA foreign_keys=OFF;
BEGIN TRANSACTION;
CREATE TABLE schema_migrations (
    version    INTEGER PRIMARY KEY CHECK (version > 0),
    name       TEXT    NOT NULL,
    checksum   TEXT    NOT NULL,
    applied_at INTEGER NOT NULL
) STRICT;
INSERT INTO schema_migrations VALUES(1,'event_log','8334cf4b3b67a7522f9fa9225c7cea1defddeaadf04dd090122ca55c802070e6',1795780800000000000);
INSERT INTO schema_migrations VALUES(2,'consumers','9a49fd45d7ed1d942825902e31aae2169e429e336cea426142cd4f39d92fb4fe',1795780800000000000);
CREATE TABLE events (
    seq              INTEGER PRIMARY KEY AUTOINCREMENT,
    stream           TEXT    NOT NULL,
    stream_version   INTEGER NOT NULL CHECK (stream_version > 0),
    type             TEXT    NOT NULL,
    type_version     INTEGER NOT NULL CHECK (type_version > 0),
    envelope_version INTEGER NOT NULL CHECK (envelope_version > 0),
    occurred_at      INTEGER NOT NULL, -- Unix nanoseconds, UTC
    actor            TEXT    NOT NULL,
    payload          TEXT    NOT NULL CHECK (json_valid(payload)),
    UNIQUE (stream, stream_version)
) STRICT;
INSERT INTO events VALUES(1,'daemon',1,'daemon.started',1,1,1795780800000000000,'daemon','{"version":"0.6.0","api_version":1,"pid":4242}');
INSERT INTO events VALUES(2,'run/acme/f1#1',1,'run.queued',1,1,1795780800000000000,'daemon','{"org":"acme","project":"web","flight":"f1","generation":1,"goal":"fix the build","base":"main"}');
INSERT INTO events VALUES(3,'run/acme/f2#1',1,'run.queued',1,1,1795780800000000000,'daemon','{"org":"acme","project":"web","flight":"f2","generation":1,"goal":"update the docs","base":"main"}');
INSERT INTO events VALUES(4,'run/acme/f1#1',2,'run.leased',1,1,1795780800000000000,'runner/acme-host-1','{"runner":"acme-host-1","placement":"host","expires_at":"2026-11-27T12:01:00Z"}');
INSERT INTO events VALUES(5,'run/acme/f1#1',3,'run.event',1,1,1795780820000000000,'runner/acme-host-1','{"fence":4,"seq":1,"kind":"output","text":"building"}');
INSERT INTO events VALUES(6,'run/acme/f1#1',4,'run.event',1,1,1795780820000000000,'runner/acme-host-1','{"fence":4,"seq":2,"kind":"activity","text":"ran the tests"}');
INSERT INTO events VALUES(7,'run/acme/f1#1',5,'run.renewed',1,1,1795780820000000000,'runner/acme-host-1','{"fence":4,"expires_at":"2026-11-27T12:01:20Z"}');
INSERT INTO events VALUES(8,'daemon',2,'daemon.stopped',1,1,1795780830000000000,'daemon','{"pid":4242,"reason":"signal"}');
CREATE TABLE streams (
    id      TEXT    PRIMARY KEY,
    version INTEGER NOT NULL CHECK (version > 0)
) STRICT;
INSERT INTO streams VALUES('daemon',2);
INSERT INTO streams VALUES('run/acme/f1#1',5);
INSERT INTO streams VALUES('run/acme/f2#1',1);
CREATE TABLE projections (
    name     TEXT    PRIMARY KEY,
    version  INTEGER NOT NULL CHECK (version > 0),
    last_seq INTEGER NOT NULL CHECK (last_seq >= 0)
) STRICT;
INSERT INTO projections VALUES('resource_status',2,8);
INSERT INTO projections VALUES('runner_runs',5,8);
INSERT INTO projections VALUES('sandbox_held',1,8);
INSERT INTO projections VALUES('capabilities',2,8);
INSERT INTO projections VALUES('audit',1,8);
CREATE TABLE consumers (
    name       TEXT    PRIMARY KEY,
    generation INTEGER NOT NULL CHECK (generation > 0),
    cursor_seq INTEGER NOT NULL CHECK (cursor_seq >= 0),
    updated_at INTEGER NOT NULL -- Unix nanoseconds, UTC
) STRICT;
CREATE TABLE resource_status (
			namespace   TEXT NOT NULL,
			kind        TEXT NOT NULL,
			name        TEXT NOT NULL,
			api_version TEXT NOT NULL,
			source      TEXT NOT NULL,
			spec_sha256 TEXT NOT NULL,
			conditions  TEXT NOT NULL,
			seq         INTEGER NOT NULL,
			PRIMARY KEY (namespace, kind, name)
		) STRICT;
CREATE TABLE config_problems (
			id        INTEGER PRIMARY KEY CHECK (id = 1),
			problems  TEXT NOT NULL,
			truncated INTEGER NOT NULL,
			seq       INTEGER NOT NULL
		) STRICT;
CREATE TABLE runner_runs (
			run         TEXT PRIMARY KEY,
			version     INTEGER NOT NULL,
			queued_seq  INTEGER NOT NULL,
			org         TEXT NOT NULL,
			project     TEXT NOT NULL,
			flight      TEXT NOT NULL,
			generation  INTEGER NOT NULL,
			goal        TEXT NOT NULL,
			base        TEXT NOT NULL,
			state       TEXT NOT NULL,
			runner      TEXT NOT NULL DEFAULT '',
			placement   TEXT NOT NULL DEFAULT '',
			fence       INTEGER NOT NULL DEFAULT 0,
			expires_ns  INTEGER NOT NULL DEFAULT 0,
			accepted    INTEGER NOT NULL DEFAULT 0,
			activity_ns INTEGER NOT NULL,
			asking      INTEGER NOT NULL DEFAULT 0,
			question    TEXT NOT NULL DEFAULT '',
			question_id TEXT NOT NULL DEFAULT '',
			given       TEXT NOT NULL DEFAULT '',
			answered    TEXT NOT NULL DEFAULT '',
			stop        INTEGER NOT NULL DEFAULT 0,
			pr          INTEGER NOT NULL DEFAULT 0,
			reason      TEXT NOT NULL DEFAULT '',
			ended_ns    INTEGER NOT NULL DEFAULT 0
		);
INSERT INTO runner_runs VALUES('f1#1',5,2,'acme','web','f1',1,'fix the build','main','leased','acme-host-1','host',4,1795780880000000000,2,1795780820000000000,0,'','','','',0,0,'',0);
INSERT INTO runner_runs VALUES('f2#1',1,3,'acme','web','f2',1,'update the docs','main','queued','','',0,0,0,1795780800000000000,0,'','','','',0,0,'',0);
CREATE TABLE runner_holds (
			org        TEXT PRIMARY KEY,
			expires_ns INTEGER NOT NULL
		);
CREATE TABLE sandbox_held (
			org           TEXT NOT NULL,
			op            TEXT NOT NULL,
			decision_seq  INTEGER NOT NULL,
			outcome       TEXT NOT NULL,
			live_sessions TEXT NOT NULL,
			since_ns      INTEGER NOT NULL,
			PRIMARY KEY (org, op)
		);
CREATE TABLE capabilities (
			id             TEXT PRIMARY KEY,
			digest         TEXT NOT NULL UNIQUE,
			org            TEXT NOT NULL,
			run            TEXT NOT NULL,
			fence          INTEGER NOT NULL,
			routes         TEXT NOT NULL,
			issued_ns      INTEGER NOT NULL,
			start_ns       INTEGER NOT NULL,
			expires_ns     INTEGER NOT NULL,
			revoked        INTEGER NOT NULL DEFAULT 0,
			revoked_reason TEXT NOT NULL DEFAULT ''
		);
CREATE TABLE audit_entries (
			seq   INTEGER PRIMARY KEY,
			id    TEXT NOT NULL UNIQUE,
			hash  TEXT NOT NULL,
			entry TEXT NOT NULL
		) STRICT;
INSERT INTO audit_entries VALUES(1,'lucy-event-1','088eb3835c4d8b46ba56de3f80cf2df91276a42757f09fd0caa60fe895a2da73','{"action":"daemon.started","actor":"daemon","decision":"allowed","details":{"actorType":"service-account","event":{"payloadSha256":"2fed690a6dedc728f7449029df5175f98f2fa12a83b9de017cb755da82e52bf1","seq":1,"stream":"daemon","streamVersion":1,"type":"daemon.started","typeVersion":1}},"hash":"088eb3835c4d8b46ba56de3f80cf2df91276a42757f09fd0caa60fe895a2da73","id":"lucy-event-1","resource":"stream/daemon","schemaVersion":"2","timestamp":"2026-11-27T12:00:00.000Z"}');
INSERT INTO audit_entries VALUES(2,'lucy-event-2','e7f08d7f75fa73a4aace902a2b86e6f35d3447c782b7d1a2c2ccd39fe502f524','{"action":"run.queued","actor":"daemon","decision":"allowed","details":{"actorType":"service-account","event":{"payloadSha256":"3527924cf53595c7c5904808df80f66435078cbf5a91fd34baa3679d40a1443e","seq":2,"stream":"run/acme/f1#1","streamVersion":1,"type":"run.queued","typeVersion":1}},"hash":"e7f08d7f75fa73a4aace902a2b86e6f35d3447c782b7d1a2c2ccd39fe502f524","id":"lucy-event-2","previousHash":"088eb3835c4d8b46ba56de3f80cf2df91276a42757f09fd0caa60fe895a2da73","resource":"stream/run/acme/f1#1","schemaVersion":"2","timestamp":"2026-11-27T12:00:00.000Z"}');
INSERT INTO audit_entries VALUES(3,'lucy-event-3','9960da576bfad74373b697510fa164bfb379051905bb51c940cd9fcf1191395a','{"action":"run.queued","actor":"daemon","decision":"allowed","details":{"actorType":"service-account","event":{"payloadSha256":"ebf942116ac70f76157d2e8374dccd2cd771f1d30f390fbc49be0e622814634c","seq":3,"stream":"run/acme/f2#1","streamVersion":1,"type":"run.queued","typeVersion":1}},"hash":"9960da576bfad74373b697510fa164bfb379051905bb51c940cd9fcf1191395a","id":"lucy-event-3","previousHash":"e7f08d7f75fa73a4aace902a2b86e6f35d3447c782b7d1a2c2ccd39fe502f524","resource":"stream/run/acme/f2#1","schemaVersion":"2","timestamp":"2026-11-27T12:00:00.000Z"}');
INSERT INTO audit_entries VALUES(4,'lucy-event-4','a41ca233761dd74cd3f05bbc343c35f8bc4720fac74e1d98fd1c1c0d7288d7a9','{"action":"run.leased","actor":"runner/acme-host-1","decision":"allowed","details":{"actorType":"ai-agent","event":{"payloadSha256":"336a363ee2ca574a3da75f3fb0d4d4a70ce9748f17f3377885d0f5b0ec700d05","seq":4,"stream":"run/acme/f1#1","streamVersion":2,"type":"run.leased","typeVersion":1}},"hash":"a41ca233761dd74cd3f05bbc343c35f8bc4720fac74e1d98fd1c1c0d7288d7a9","id":"lucy-event-4","previousHash":"9960da576bfad74373b697510fa164bfb379051905bb51c940cd9fcf1191395a","resource":"stream/run/acme/f1#1","schemaVersion":"2","timestamp":"2026-11-27T12:00:00.000Z"}');
INSERT INTO audit_entries VALUES(5,'lucy-event-5','ac87fe4f5fc2489bd9e73ae71eb0886bb29d8e1d119a4dcc3eb5f7c320493857','{"action":"run.event","actor":"runner/acme-host-1","decision":"allowed","details":{"actorType":"ai-agent","event":{"payloadSha256":"91425f858f244797083e0cbfc37d34cf9226812a678904d8a7e7374a6ad776e3","seq":5,"stream":"run/acme/f1#1","streamVersion":3,"type":"run.event","typeVersion":1}},"hash":"ac87fe4f5fc2489bd9e73ae71eb0886bb29d8e1d119a4dcc3eb5f7c320493857","id":"lucy-event-5","previousHash":"a41ca233761dd74cd3f05bbc343c35f8bc4720fac74e1d98fd1c1c0d7288d7a9","resource":"stream/run/acme/f1#1","schemaVersion":"2","timestamp":"2026-11-27T12:00:20.000Z"}');
INSERT INTO audit_entries VALUES(6,'lucy-event-6','005a23a610f508ab2fccfab7cf6a3b2ec49d76f96c07f157eab90ce6fbcaf099','{"action":"run.event","actor":"runner/acme-host-1","decision":"allowed","details":{"actorType":"ai-agent","event":{"payloadSha256":"4ad5cdf191707bf7f5572c3b92fe8e4861dca672a5647197fc462e6b7760792c","seq":6,"stream":"run/acme/f1#1","streamVersion":4,"type":"run.event","typeVersion":1}},"hash":"005a23a610f508ab2fccfab7cf6a3b2ec49d76f96c07f157eab90ce6fbcaf099","id":"lucy-event-6","previousHash":"ac87fe4f5fc2489bd9e73ae71eb0886bb29d8e1d119a4dcc3eb5f7c320493857","resource":"stream/run/acme/f1#1","schemaVersion":"2","timestamp":"2026-11-27T12:00:20.000Z"}');
INSERT INTO audit_entries VALUES(7,'lucy-event-7','d2491e24794fbeb90a6d8d699aa755075545f929a5c87203724777b79a6c8143','{"action":"run.renewed","actor":"runner/acme-host-1","decision":"allowed","details":{"actorType":"ai-agent","event":{"payloadSha256":"46e0e8521ba8fb010470c3dca3ba8ab62eafc7350653f49855e3d4fdb0087159","seq":7,"stream":"run/acme/f1#1","streamVersion":5,"type":"run.renewed","typeVersion":1}},"hash":"d2491e24794fbeb90a6d8d699aa755075545f929a5c87203724777b79a6c8143","id":"lucy-event-7","previousHash":"005a23a610f508ab2fccfab7cf6a3b2ec49d76f96c07f157eab90ce6fbcaf099","resource":"stream/run/acme/f1#1","schemaVersion":"2","timestamp":"2026-11-27T12:00:20.000Z"}');
INSERT INTO audit_entries VALUES(8,'lucy-event-8','7b1b632ca805d3c2bce0c199b247191e091b4aaaf39b1585d3b113ac056af833','{"action":"daemon.stopped","actor":"daemon","decision":"allowed","details":{"actorType":"service-account","event":{"payloadSha256":"cb2a542c53d3dcd4e25baac8492914db18907d2f331b15288715479dd6bd8cd0","seq":8,"stream":"daemon","streamVersion":2,"type":"daemon.stopped","typeVersion":1}},"hash":"7b1b632ca805d3c2bce0c199b247191e091b4aaaf39b1585d3b113ac056af833","id":"lucy-event-8","previousHash":"d2491e24794fbeb90a6d8d699aa755075545f929a5c87203724777b79a6c8143","resource":"stream/daemon","schemaVersion":"2","timestamp":"2026-11-27T12:00:30.000Z"}');
PRAGMA writable_schema=ON;
CREATE TABLE IF NOT EXISTS sqlite_sequence(name,seq);
DELETE FROM sqlite_sequence;
INSERT INTO sqlite_sequence VALUES('events',8);
CREATE TRIGGER events_append_only_update BEFORE UPDATE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;
CREATE TRIGGER events_append_only_delete BEFORE DELETE ON events
BEGIN
    SELECT RAISE(ABORT, 'events are append-only');
END;
CREATE TRIGGER audit_entries_no_update BEFORE UPDATE ON audit_entries
			BEGIN SELECT RAISE(ABORT, 'audit entries are append-only'); END;
CREATE TRIGGER audit_entries_no_delete BEFORE DELETE ON audit_entries
			BEGIN SELECT RAISE(ABORT, 'audit entries are append-only'); END;
CREATE INDEX runner_runs_state ON runner_runs (state, queued_seq);
CREATE INDEX capabilities_issued ON capabilities (issued_ns);
CREATE INDEX capabilities_run ON capabilities (run, fence);
PRAGMA writable_schema=OFF;
COMMIT;
