"""Project JSON or console diagnostic events to fixed, non-sensitive fields."""
import json
import re

ROUTES = {'shares', 'helper_status', 'delegate_vote', 'cast_vote', 'cast_vote_batch', 'delegate_and_cast_vote_batch', 'chain_status'}
NUMBERS = {'started_unix_us', 'duration_us', 'headers_us', 'body_read_us', 'body_bytes', 'response_write_us', 'response_bytes', 'write_errors', 'status', 'after_us', 'attempt'}
ENUMS = {'method': {'GET', 'POST'}, 'protocol': {'HTTP/1.0', 'HTTP/1.1', 'HTTP/2.0', 'HTTP/3.0'}, 'phase': {'handler_entry', 'broadcast', 'status_lookup'}, 'outcome': {'ok', 'error', 'context_done'}}


def application_record(message):
    # journalctl -o json uses a byte array for messages containing ANSI escapes.
    if isinstance(message, list):
        if len(message) > 65536 or not all(type(value) is int and 0 <= value <= 255 for value in message):
            return None
        try:
            message = bytes(message).decode('utf-8')
        except UnicodeDecodeError:
            return None
    if not isinstance(message, str):
        return None
    try:
        record = json.loads(message)
        if not isinstance(record, dict):
            return None
        event = record.get('message', record.get('msg'))
    except ValueError:
        text = re.sub(r'\x1b\[[0-9;]*m', '', message)
        event = next((event for event in ('vote HTTP timing', 'vote HTTP phase') if event in text), None)
        record = dict(re.findall(r'(?:^|\s)([a-z_]+)=("[^"\n]*"|[^\s]+)', text))
        record = {key: value.strip('"') for key, value in record.items()}
    if event not in ('vote HTTP timing', 'vote HTTP phase'):
        return None
    identifier = record.get('request_id')
    if not isinstance(identifier, str) or not re.fullmatch('[0-9a-f]{32}', identifier) or not isinstance(record.get('route'), str) or record['route'] not in ROUTES:
        return None
    projected = {'event': event, 'request_id': identifier, 'route': record['route']}
    for key in NUMBERS:
        value = record.get(key)
        if isinstance(value, bool):
            continue
        if isinstance(value, int) and value >= 0:
            projected[key] = value
        elif isinstance(value, str) and re.fullmatch('[0-9]+', value):
            projected[key] = int(value)
    for key, allowed in ENUMS.items():
        if isinstance(record.get(key), str) and record[key] in allowed:
            projected[key] = record[key]
    complete = record.get('body_complete')
    if isinstance(complete, bool):
        projected['body_complete'] = complete
    elif complete in ('true', 'false'):
        projected['body_complete'] = complete == 'true'
    return projected
