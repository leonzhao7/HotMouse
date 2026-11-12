#!/usr/bin/env python3
"""Read-only competition_005 analysis; write redacted metrics and local transcripts.

No network requests, submissions, target execution, or database mutations.
Usage: python3 tools/competition_005_analyze.py --output-dir docs
"""
import argparse
import collections
import csv
import datetime as dt
import json
from pathlib import Path
import re
import sqlite3
import statistics

ROOT = Path(__file__).resolve().parents[1]


def timestamp(value):
    if isinstance(value, (int, float)):
        return dt.datetime.fromtimestamp(value / 1000, dt.timezone.utc)
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00')) if value else None


def seconds(start, end):
    return (timestamp(end) - timestamp(start)).total_seconds() if start and end else 0


def norm(value):
    return re.sub(r'\s+', '', value)


def content_text(message):
    return '\n'.join(b.get('text', '') for b in message.get('content', [])
                     if b.get('type') == 'text')


def stage(prompt, fallback='unknown'):
    head = prompt[:600].lower()
    if 'background target analyst' in head or 'startup target analysis' in head:
        return 'target_probe'
    if 'graph decision worker' in head:
        return 'decide'
    if 'bounded step execution worker' in head:
        return 'execute'
    if any(term in head for term in ['summar', 'conclud', 'interrupted worker', 'saved session']):
        return 'dispatch_commit'
    return fallback


def union_seconds(intervals):
    total = 0
    end = None
    for a, b in sorted(intervals):
        if end is None or a > end:
            total += (b - a).total_seconds()
            end = b
        elif b > end:
            total += (b - end).total_seconds()
            end = b
    return total


def write_csv(path, rows, redact=lambda value: value):
    if not rows:
        return
    with path.open('w', newline='') as file:
        writer = csv.DictWriter(file, fieldnames=list(rows[0]))
        writer.writeheader()
        writer.writerows(
            {key: redact(value) if isinstance(value, str) else value for key, value in row.items()}
            for row in rows
        )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output-dir', type=Path, default=ROOT / 'docs')
    parser.add_argument('--transcript-dir', type=Path, default=Path('/tmp/hotmouse-competition005-analysis'))
    parser.add_argument('--reference-dir', type=Path, default=Path('/home/leon/workspace/cybench/cybench-38929'))
    args = parser.parse_args()
    args.output_dir.mkdir(parents=True, exist_ok=True)
    args.transcript_dir.mkdir(parents=True, exist_ok=True)
    source = sqlite3.connect(f'file:{ROOT / "backend/data/hotmouse.db"}?mode=ro', uri=True)
    db = sqlite3.connect(':memory:')
    source.backup(db)
    source.close()
    db.row_factory = sqlite3.Row
    run = dict(db.execute("SELECT * FROM competition_runs WHERE id='competition_005'").fetchone())
    secrets = set(re.findall(r'\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b', run['rules'], re.I))
    for value, in db.execute('SELECT value FROM flags'):
        if value:
            secrets.add(value)
    secrets = sorted(secrets, key=len, reverse=True)

    uuid_pattern = re.compile(r'(?<![0-9a-f])[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}(?![0-9a-f])', re.I)
    jwt_pattern = re.compile(r'\beyJ[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\.[A-Za-z0-9_-]{5,}\b')
    authorization_pattern = re.compile(
        r'(?i)(?P<prefix>\b(?:authorization|proxy-authorization)\b\s*["\']?\s*[:=]\s*)'
        r'(?:(?P<quoted>"[^"\n]*"|\'[^\'\n]*\')|'
        r'(?P<bare>(?:(?:Bearer|Basic)\s+)?[^\s,;}\]]+))')
    credential_pattern = re.compile(
        r'(?i)(\b(?:api[_-]?key|access[_-]?token|'
        r'refresh[_-]?token|id[_-]?token|token|jwt|password|passwd|secret|'
        r'private[_-]?key|client[_-]?secret|credential|cookie)\b\s*["\']?\s*[:=]\s*)'
        r'(?:(?P<bytes>b)?(?P<quote>["\'])(?P<quoted>[^"\'\n]*?)(?P=quote)|'
        r'(?P<bare>[^\s,;}\]]+))')

    def redact(value):
        value = str(value)
        for secret in secrets:
            value = value.replace(secret, '[REDACTED]')
        value = uuid_pattern.sub('[REDACTED-UUID]', value)
        value = jwt_pattern.sub('[REDACTED-JWT]', value)

        def redact_authorization(match):
            quoted = match.group('quoted')
            replacement = quoted[0] + '[REDACTED]' + quoted[0] if quoted else '[REDACTED]'
            return match.group('prefix') + replacement

        value = authorization_pattern.sub(redact_authorization, value)

        def redact_credential(match):
            prefix = match.group(1)
            if match.group('quote'):
                return prefix + (match.group('bytes') or '') + match.group('quote') + '[REDACTED]' + match.group('quote')
            return prefix + '[REDACTED]'

        value = credential_pattern.sub(redact_credential, value)
        value = re.sub(r'(?i)\b(?:flag|HTB|gctf|SEKAI|hkcert22)\{[^}\n]{1,1000}\}', '[FLAG]', value)
        return re.sub(r'(?i)\b(Bearer|Basic)\s+[A-Za-z0-9._~+/-]{8,}={0,2}', r'\1 [REDACTED]', value)

    items = [dict(r) for r in db.execute("SELECT * FROM competition_items WHERE run_id=? ORDER BY ordinal", (run['id'],))]
    metadata = {i['project_id']: json.loads(db.execute('SELECT challenge_json FROM platform_projects WHERE project_id=?', (i['project_id'],)).fetchone()[0]) for i in items}
    histories = [dict(r) for r in db.execute("SELECT h.* FROM worker_task_history h JOIN competition_items i ON i.project_id=h.project_id WHERE i.run_id=? ORDER BY h.started_at,h.id", (run['id'],))]
    for h in histories:
        h['elapsed_seconds'] = round(seconds(h['started_at'], h['finished_at'] or h['last_activity_at']), 3)
        h['detail'] = redact(h['detail'])
    write_csv(args.output_dir / 'competition_005_tasks.csv', histories, redact)

    reference = collections.defaultdict(list)
    for path in sorted(args.reference_dir.glob('*.json')):
        if path.name == 'list.json':
            continue
        data = json.loads(path.read_text())
        candidates = [i['challenge_id'] for i in items if norm(metadata[i['project_id']]['description'])[:45] in norm(data['session']['title'])]
        assert len(candidates) <= 1, (path, candidates)
        challenge = candidates[0] if candidates else 'supervisor'
        reference[challenge].append(data)
    assert set(reference) == {i['challenge_id'] for i in items} | {'supervisor'}
    ref_rows = []
    for challenge, sessions in sorted(reference.items()):
        calls = []
        accepted_reports = []
        transcript = []
        for data in sessions:
            session = data['session']
            transcript.append(f"SESSION {redact(session['id'])} {session['first_captured_at']} {session['last_active_at']}")
            flag_call_ids = set()
            for event in data['steps']:
                parts = []
                for block in event['items']:
                    kind = block['kind']
                    if kind == 'tool_call':
                        calls.append((event['captured_at'], block['name'], session['id'], event['seq']))
                        if block['name'] == 'report_flag':
                            flag_call_ids.add(block['call_id'])
                        parts.append(block['name'] + ' ' + redact(json.dumps(block['args'], ensure_ascii=False))[:6000])
                    elif kind in ['tool_result', 'assistant_text', 'thinking']:
                        parts.append(redact(block['text'])[:6000])
                        if kind == 'tool_result' and block['call_id'] in flag_call_ids and 'flag 已齐' in block['text']:
                            accepted_reports.append(event['captured_at'])
                if parts:
                    transcript.append(f"seq={event['seq']} {event['captured_at']} {event['event_type']}\n" + '\n'.join(parts))
        (args.transcript_dir / f'{challenge}-reference.txt').write_text('\n'.join(transcript))
        first = min(s['session']['first_captured_at'] for s in sessions)
        last = max(s['session']['last_active_at'] for s in sessions)
        flag_calls = [c for c in calls if c[1] == 'report_flag']
        first_flag = min(flag_calls, default=('', '', '', ''), key=lambda c: c[0])
        ref_rows.append({
            'challenge_id': challenge, 'session_files': len(sessions),
            'first_at': first, 'last_at': last, 'span_seconds': seconds(first, last),
            'complete_exports': sum(len(s['steps']) == s['pagination']['total'] for s in sessions),
            'visible_events': sum(len(s['steps']) for s in sessions),
            'reported_total_events': sum(s['pagination']['total'] for s in sessions),
            'tool_calls': len(calls), 'report_flag_calls': len(flag_calls),
            'request_hint_calls': sum(c[1] == 'request_hint' for c in calls),
            'explicit_all_flags_results': len(accepted_reports),
            'first_report_flag_at': first_flag[0],
            'first_report_flag_seconds': seconds(first, first_flag[0]) if flag_calls else '',
            'first_report_session': first_flag[2], 'first_report_seq': first_flag[3],
            'sessions': ';'.join(str(s['session']['id']) for s in sessions),
        })
    write_csv(args.output_dir / 'competition_005_reference_sessions.csv', ref_rows, redact)
    ref_by_challenge = {r['challenge_id']: r for r in ref_rows}

    requests = []
    metrics = []
    all_tool_intervals = []
    all_llm_intervals = []
    all_task_intervals = []
    for item in items:
        project = item['project_id']
        meta = metadata[project]
        task_rows = [h for h in histories if h['project_id'] == project]
        flags = [dict(r) for r in db.execute('SELECT f.id,f.status,f.value,f.created_at,MIN(a.finished_at) accepted_at FROM flags f LEFT JOIN flag_attempts a ON a.flag_id=f.id AND a.status=\'accepted\' WHERE f.project_id=? GROUP BY f.id', (project,))]
        accepted = [f for f in flags if f['status'] == 'accepted']
        observations = []
        calls_by_id = {}
        tool_intervals = []
        llm_intervals = []
        transcript = []
        seen_messages = set()
        session_count = 0
        tool_counts = collections.Counter()
        for path in sorted((ROOT / 'backend/data/workspaces/hotmouse' / project).rglob('*.jsonl')):
            if path.parent.name != 'pi-sessions':
                continue
            rows = [json.loads(line) for line in path.read_text().splitlines() if line.strip()]
            session_count += 1
            relative = str(path.relative_to(ROOT))
            transcript.append(f'SESSION {redact(relative)}')
            current_stage = 'unknown'
            for line_number, row in enumerate(rows, 1):
                msg = row.get('message', {})
                if not msg:
                    continue
                identity = (rows[0].get('id'), row.get('id'))
                if identity in seen_messages:
                    continue
                seen_messages.add(identity)
                role = msg.get('role')
                when = timestamp(row['timestamp'])
                if role == 'user':
                    current_stage = stage(content_text(msg), current_stage)
                    transcript.append(f'line={line_number} {row["timestamp"]} USER stage={current_stage}\n{redact(content_text(msg))[:400]}')
                elif role == 'assistant':
                    duration = msg.get('durationMs')
                    inferred = False
                    if duration is None and isinstance(msg.get('timestamp'), (int, float)):
                        duration = max(0, (when - timestamp(msg['timestamp'])).total_seconds() * 1000)
                        inferred = True
                    if duration is not None:
                        llm_intervals.append((when - dt.timedelta(milliseconds=duration), when))
                    tools_called = [b for b in msg.get('content', []) if b.get('type') == 'toolCall']
                    usage = msg.get('usage', {})
                    requests.append({
                        'challenge_id': item['challenge_id'], 'project_id': project,
                        'session_file': relative, 'line': line_number, 'stage': current_stage,
                        'finished_at': row['timestamp'], 'duration_seconds': round(duration / 1000, 3) if duration is not None else '',
                        'duration_inferred': inferred, 'stop_reason': msg.get('stopReason', ''),
                        'model': msg.get('model', ''), 'response_model': msg.get('responseModel', ''),
                        'input_tokens': usage.get('input', 0), 'cache_read_tokens': usage.get('cacheRead', 0),
                        'output_tokens': usage.get('output', 0), 'reasoning_tokens': usage.get('reasoning', 0),
                        'tool_calls': len(tools_called), 'tool_names': ';'.join(b['name'] for b in tools_called),
                        'error': redact(msg.get('errorMessage', '')),
                    })
                    for block in tools_called:
                        calls_by_id[block['id']] = when
                        tool_counts[block['name']] += 1
                    parts = [redact(b.get('text', b.get('thinking', '')))[:4500] for b in msg.get('content', []) if b.get('type') in ['text', 'thinking']]
                    parts += [b['name'] + ' ' + redact(json.dumps(b.get('arguments'), ensure_ascii=False))[:6000] for b in tools_called]
                    transcript.append(f'line={line_number} {row["timestamp"]} ASSISTANT stage={current_stage} durationMs={duration}\n' + '\n'.join(parts))
                elif role == 'toolResult':
                    call_start = calls_by_id.get(msg.get('toolCallId'))
                    if call_start:
                        tool_intervals.append((call_start, when))
                    raw_text = content_text(msg)
                    for flag in accepted:
                        if flag['value'] in raw_text:
                            observations.append((when, relative, line_number))
                    transcript.append(f'line={line_number} {row["timestamp"]} TOOL {msg.get("toolName")} error={msg.get("isError")}\n' + redact(raw_text)[:6000])
        (args.transcript_dir / f'{item["challenge_id"]}-hotmouse.txt').write_text('\n'.join(transcript))
        project_requests = [r for r in requests if r['project_id'] == project]
        durations = [r['duration_seconds'] for r in project_requests if r['duration_seconds'] != '']
        first_observation = min(observations, default=(None, '', ''), key=lambda r: r[0])
        accepted_at = min((f['accepted_at'] for f in accepted if f['accepted_at']), default='')
        task_intervals = [(timestamp(h['started_at']), timestamp(h['finished_at'] or h['last_activity_at'])) for h in task_rows if h['finished_at'] or h['last_activity_at']]
        all_task_intervals.extend(task_intervals)
        all_tool_intervals.extend(tool_intervals)
        all_llm_intervals.extend(llm_intervals)
        ref = ref_by_challenge[item['challenge_id']]
        row = {
            'challenge_id': item['challenge_id'], 'ordinal': item['ordinal'] + 1,
            'project_id': project, 'difficulty': meta['difficulty'], 'total_score': meta['total_score'],
            'status': item['status'], 'started_at': item['started_at'], 'finished_at': item['finished_at'],
            'wall_seconds': round(seconds(item['started_at'], item['finished_at']), 3),
            'global_cutoff': 'invalid_state' in item['last_error'],
            'accepted_flags': len(accepted), 'rejected_flags': sum(f['status'] == 'rejected' for f in flags),
            'accepted_at': accepted_at, 'first_tool_flag_at': first_observation[0].isoformat() if first_observation[0] else '',
            'tool_flag_to_accept_seconds': round((timestamp(accepted_at) - first_observation[0]).total_seconds(), 3) if first_observation[0] and accepted_at else '',
            'first_tool_flag_file': first_observation[1], 'first_tool_flag_line': first_observation[2],
            'pi_session_files': session_count, 'llm_responses': len(project_requests),
            'llm_duration_seconds': round(sum(durations), 3),
            'llm_duration_union_seconds': round(union_seconds(llm_intervals), 3),
            'llm_median_seconds': round(statistics.median(durations), 3) if durations else '',
            'llm_over_60_seconds': sum(d > 60 for d in durations),
            'tool_calls': sum(tool_counts.values()), 'checkpoint_calls': tool_counts['checkpoint'],
            'tool_wait_union_seconds': round(union_seconds(tool_intervals), 3),
            'task_busy_union_seconds': round(union_seconds(task_intervals), 3),
        }
        for name in ['target_probe', 'decide', 'execute', 'dispatch_commit']:
            selected = [h for h in task_rows if h['task_type'] == name]
            row[name + '_tasks'] = len(selected)
            row[name + '_seconds'] = round(sum(h['elapsed_seconds'] for h in selected), 3)
        for prior in ['001', '002', '003', '004']:
            old = db.execute('SELECT status FROM competition_items WHERE run_id=? AND challenge_id=?', ('competition_' + prior, item['challenge_id'])).fetchone()
            row['competition_' + prior] = old[0] if old else ''
        for name in ['first_report_flag_seconds', 'first_report_session', 'first_report_seq', 'request_hint_calls']:
            row['reference_' + name] = ref[name]
        metrics.append(row)
    write_csv(args.output_dir / 'competition_005_metrics.csv', metrics, redact)
    write_csv(args.output_dir / 'competition_005_llm_requests.csv', requests, redact)
    durations = [r['duration_seconds'] for r in requests if r['duration_seconds'] != '']
    summary = {
        'run': {key: run[key] for key in ['id', 'created_at', 'finished_at', 'status', 'concurrency', 'max_workers', 'challenge_timeout_seconds']},
        'status_counts': dict(collections.Counter(i['status'] for i in items)),
        'nominal_score': sum(m['total_score'] for m in metrics if m['status'] == 'succeeded'),
        'all_nominal_score': sum(m['total_score'] for m in metrics),
        'success_wall_seconds': sum(m['wall_seconds'] for m in metrics if m['status'] == 'succeeded'),
        'failure_wall_seconds': sum(m['wall_seconds'] for m in metrics if m['status'] == 'failed'),
        'task_busy_union_seconds': union_seconds(all_task_intervals),
        'observed_llm_union_seconds': union_seconds(all_llm_intervals),
        'observed_tool_wait_union_seconds': union_seconds(all_tool_intervals),
        'llm_responses': len(requests), 'llm_duration_seconds': sum(durations),
        'llm_duration_median_seconds': statistics.median(durations),
        'llm_duration_over_60_count': sum(d > 60 for d in durations),
        'llm_stages': dict(collections.Counter(r['stage'] for r in requests)),
        'local_tool_counts': dict(collections.Counter(name for r in requests for name in r['tool_names'].split(';') if name)),
        'reference_files': sum(r['session_files'] for r in ref_rows),
        'reference_complete_files': sum(r['complete_exports'] for r in ref_rows),
        'reference_events': sum(r['visible_events'] for r in ref_rows),
        'reference_report_flags': sum(r['report_flag_calls'] for r in ref_rows),
        'reference_hint_calls': sum(r['request_hint_calls'] for r in ref_rows),
        'reference_challenges_with_report_flag': sum(bool(r['report_flag_calls']) for r in ref_rows if r['challenge_id'] != 'supervisor'),
        'transcript_dir': str(args.transcript_dir),
    }
    (args.output_dir / 'competition_005_summary.json').write_text(json.dumps(summary, ensure_ascii=False, indent=2) + '\n')
    print(json.dumps(summary, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    main()
