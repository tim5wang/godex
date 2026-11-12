package workcontext

import "fmt"

// Memory revisions describe source changes, independently of UI read receipts,
// event timestamps, and Dream's own derived output.
func init() {
	statements := []string{
		`ALTER TABLE wc_facts ADD COLUMN valid_until TEXT NOT NULL DEFAULT ''`,
		`CREATE TABLE wc_session_bindings(session_id TEXT PRIMARY KEY, tenant_id TEXT NOT NULL, owner_id TEXT NOT NULL, context_id TEXT NOT NULL)`,
		`INSERT INTO wc_session_bindings SELECT session_id,tenant_id,owner_id,context_id FROM (
   SELECT s.session_id,c.tenant_id,c.owner_id,c.id context_id,ROW_NUMBER() OVER(PARTITION BY s.session_id ORDER BY s.created_at,s.id) n
   FROM wc_context_sessions s JOIN wc_contexts c ON c.tenant_id=s.tenant_id AND c.id=s.context_id
   WHERE NOT EXISTS(SELECT 1 FROM wc_context_sessions x JOIN wc_contexts y ON y.tenant_id=x.tenant_id AND y.id=x.context_id WHERE x.session_id=s.session_id AND (y.tenant_id<>c.tenant_id OR y.owner_id<>c.owner_id))
  ) WHERE n=1`,
		`CREATE TABLE wc_memory_state(tenant_id TEXT NOT NULL,context_id TEXT NOT NULL,source_seq INTEGER NOT NULL DEFAULT 0,processed_seq INTEGER NOT NULL DEFAULT 0,summary TEXT NOT NULL DEFAULT '',enabled INTEGER NOT NULL DEFAULT 1,interval_seconds INTEGER NOT NULL DEFAULT 21600,next_run_at TEXT NOT NULL DEFAULT '',run_id TEXT NOT NULL DEFAULT '',lease_until TEXT NOT NULL DEFAULT '',status TEXT NOT NULL DEFAULT 'idle',last_error TEXT NOT NULL DEFAULT '',failure_count INTEGER NOT NULL DEFAULT 0,rescan_cursor INTEGER NOT NULL DEFAULT -1,rescan_through INTEGER NOT NULL DEFAULT 0,PRIMARY KEY(tenant_id,context_id))`,
		`CREATE TABLE wc_memory_changes(tenant_id TEXT NOT NULL,context_id TEXT NOT NULL,seq INTEGER NOT NULL,kind TEXT NOT NULL,entity_id TEXT NOT NULL,PRIMARY KEY(tenant_id,context_id,seq))`,
		`CREATE TABLE wc_memory_runs(id TEXT PRIMARY KEY,tenant_id TEXT NOT NULL,context_id TEXT NOT NULL,through_seq INTEGER NOT NULL,status TEXT NOT NULL,mode TEXT NOT NULL DEFAULT 'manual',input_json TEXT NOT NULL DEFAULT '{}',result_json TEXT NOT NULL DEFAULT '{}',error TEXT NOT NULL DEFAULT '',created_at TEXT NOT NULL,completed_at TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE wc_memory_guard(tenant_id TEXT NOT NULL,context_id TEXT NOT NULL,PRIMARY KEY(tenant_id,context_id))`,
		`INSERT INTO wc_memory_state(tenant_id,context_id,source_seq,next_run_at) SELECT tenant_id,id,1,strftime('%Y-%m-%dT%H:%M:%S','now','+6 hours')||'.000000000Z' FROM wc_contexts`,
		`INSERT INTO wc_memory_changes SELECT tenant_id,id,1,'context',id FROM wc_contexts`,
		`INSERT INTO wc_memory_changes SELECT tenant_id,context_id,1+ROW_NUMBER() OVER(PARTITION BY tenant_id,context_id ORDER BY created_at,id),'fact',id FROM wc_facts`,
		`UPDATE wc_memory_state SET source_seq=1+(SELECT COUNT(*) FROM wc_facts f WHERE f.tenant_id=wc_memory_state.tenant_id AND f.context_id=wc_memory_state.context_id)`,
		`CREATE TRIGGER wc_memory_context_create AFTER INSERT ON wc_contexts BEGIN INSERT INTO wc_memory_state(tenant_id,context_id,source_seq,next_run_at) VALUES(NEW.tenant_id,NEW.id,1,strftime('%Y-%m-%dT%H:%M:%S','now','+6 hours')||'.000000000Z'); INSERT INTO wc_memory_changes VALUES(NEW.tenant_id,NEW.id,1,'context',NEW.id); END`,
	}
	// Existing desktops need a complete initial source inventory too; otherwise
	// pre-migration plans and resources would remain invisible until edited.
	for _, source := range []struct{ table, kind, entity string }{
		{"wc_todos", "todo", "id"}, {"wc_context_tasks", "delegation", "id"},
		{"wc_context_resources", "resource", "resource_id"}, {"wc_context_sessions", "session", "id"}, {"wc_relations", "relation", "id"},
	} {
		statements = append(statements, fmt.Sprintf(`INSERT INTO wc_memory_changes SELECT x.tenant_id,x.context_id,m.source_seq+ROW_NUMBER() OVER(PARTITION BY x.tenant_id,x.context_id ORDER BY x.created_at,x.%s),'%s',x.%s FROM %s x JOIN wc_memory_state m ON m.tenant_id=x.tenant_id AND m.context_id=x.context_id`, source.entity, source.kind, source.entity, source.table),
			`UPDATE wc_memory_state SET source_seq=(SELECT MAX(seq) FROM wc_memory_changes x WHERE x.tenant_id=wc_memory_state.tenant_id AND x.context_id=wc_memory_state.context_id)`)
	}
	// Guard rows only exist inside the transaction applying a Dream result.
	// External writes cannot inherit that guard, even from another conversation.
	change := func(table, op, ref, kind, condition, entity string) {
		statements = append(statements, fmt.Sprintf(`CREATE TRIGGER wc_memory_%s_%s AFTER %s ON %s
   WHEN (%s) AND NOT EXISTS(SELECT 1 FROM wc_memory_guard WHERE tenant_id=%s.tenant_id AND context_id=%s.context_id)
   BEGIN UPDATE wc_memory_state SET source_seq=source_seq+1 WHERE tenant_id=%s.tenant_id AND context_id=%s.context_id;
   INSERT INTO wc_memory_changes SELECT tenant_id,context_id,source_seq,'%s',%s.%s FROM wc_memory_state WHERE tenant_id=%s.tenant_id AND context_id=%s.context_id; END`, table, op, op, table, condition, ref, ref, ref, ref, kind, ref, entity, ref, ref))
	}
	for _, op := range []string{"INSERT", "UPDATE", "DELETE"} {
		ref := "NEW"
		if op == "DELETE" {
			ref = "OLD"
		}
		cond := "1"
		if op == "UPDATE" {
			cond = `OLD.title<>NEW.title OR OLD.content<>NEW.content OR OLD.status<>NEW.status OR OLD.scope<>NEW.scope OR OLD.source<>NEW.source OR OLD.source_revision<>NEW.source_revision OR OLD.observed_at<>NEW.observed_at OR OLD.verified_at<>NEW.verified_at OR OLD.supersedes_id<>NEW.supersedes_id OR OLD.valid_until<>NEW.valid_until OR OLD.version<>NEW.version`
		}
		change("wc_facts", op, ref, "fact", cond, "id")
		cond = "1"
		if op == "UPDATE" {
			cond = `OLD.text<>NEW.text OR OLD.status<>NEW.status OR OLD.assignee<>NEW.assignee OR OLD.due_at<>NEW.due_at`
		}
		change("wc_todos", op, ref, "todo", cond, "id")
		cond = "1"
		if op == "UPDATE" {
			cond = `OLD.state<>NEW.state OR OLD.title<>NEW.title OR OLD.pack<>NEW.pack OR OLD.card_id<>NEW.card_id`
		}
		change("wc_context_tasks", op, ref, "delegation", cond, "id")
		cond = "1"
		if op == "UPDATE" {
			cond = `OLD.purpose<>NEW.purpose`
		}
		change("wc_context_resources", op, ref, "resource", cond, "resource_id")
		cond = "1"
		if op == "UPDATE" {
			cond = `OLD.topic<>NEW.topic`
		}
		change("wc_context_sessions", op, ref, "session", cond, "id")
		cond = "1"
		if op == "UPDATE" {
			cond = `OLD.kind<>NEW.kind OR OLD.condition<>NEW.condition`
		}
		change("wc_relations", op, ref, "relation", cond, "id")
	}
	// A resource can be referenced by several desktops; edits dirty only those
	// references. Merely updating its UI timestamp is not a semantic change.
	statements = append(statements, `CREATE TRIGGER wc_memory_resource_update AFTER UPDATE ON wc_resources
 WHEN OLD.title<>NEW.title OR OLD.kind<>NEW.kind OR OLD.locator<>NEW.locator OR OLD.node_id<>NEW.node_id OR OLD.purpose<>NEW.purpose
 BEGIN UPDATE wc_memory_state SET source_seq=source_seq+1 WHERE tenant_id=NEW.tenant_id AND context_id IN (SELECT context_id FROM wc_context_resources WHERE tenant_id=NEW.tenant_id AND resource_id=NEW.id);
 INSERT INTO wc_memory_changes SELECT tenant_id,context_id,source_seq,'resource',NEW.id FROM wc_memory_state WHERE tenant_id=NEW.tenant_id AND context_id IN (SELECT context_id FROM wc_context_resources WHERE tenant_id=NEW.tenant_id AND resource_id=NEW.id); END`)
	statements = append(statements, `CREATE TRIGGER wc_memory_context_update AFTER UPDATE ON wc_contexts
 WHEN (OLD.title<>NEW.title OR OLD.goal<>NEW.goal OR OLD.background<>NEW.background OR OLD.status<>NEW.status
 OR COALESCE(json_extract(OLD.checkpoint_json,'$.summary'),'')<>COALESCE(json_extract(NEW.checkpoint_json,'$.summary'),'')
 OR COALESCE(json_extract(OLD.checkpoint_json,'$.completed'),'[]')<>COALESCE(json_extract(NEW.checkpoint_json,'$.completed'),'[]')
 OR COALESCE(json_extract(OLD.checkpoint_json,'$.in_progress'),'[]')<>COALESCE(json_extract(NEW.checkpoint_json,'$.in_progress'),'[]')
 OR COALESCE(json_extract(OLD.checkpoint_json,'$.next_actions'),'[]')<>COALESCE(json_extract(NEW.checkpoint_json,'$.next_actions'),'[]')
 OR COALESCE(json_extract(OLD.checkpoint_json,'$.blockers'),'[]')<>COALESCE(json_extract(NEW.checkpoint_json,'$.blockers'),'[]')
 OR COALESCE(json_extract(OLD.checkpoint_json,'$.open_questions'),'[]')<>COALESCE(json_extract(NEW.checkpoint_json,'$.open_questions'),'[]')
 OR COALESCE(json_extract(OLD.checkpoint_json,'$.evidence_refs'),'[]')<>COALESCE(json_extract(NEW.checkpoint_json,'$.evidence_refs'),'[]'))
 AND NOT EXISTS(SELECT 1 FROM wc_memory_guard WHERE tenant_id=NEW.tenant_id AND context_id=NEW.id)
 BEGIN UPDATE wc_memory_state SET source_seq=source_seq+1 WHERE tenant_id=NEW.tenant_id AND context_id=NEW.id;
 INSERT INTO wc_memory_changes SELECT tenant_id,context_id,source_seq,'context',NEW.id FROM wc_memory_state WHERE tenant_id=NEW.tenant_id AND context_id=NEW.id; END`)
	migrations[7] = statements
}
