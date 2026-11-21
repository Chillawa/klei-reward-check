import base64
import hashlib
import json
import re
from datetime import date, datetime, timedelta, timezone

BSKY_HANDLE = 'kleientertainment.bsky.social'
BSKY_DID = 'did:plc:bm3fpb7we4xj6sxumouyy4wk'
PDS = 'https://oyster.us-east.host.bsky.network'
RPC = PDS + '/xrpc/com.atproto.'
BSKY_POSTS = RPC + 'repo.listRecords?repo=' + BSKY_DID + '&collection=app.bsky.feed.post&limit=12'
BSKY_IDENTITY = RPC + 'repo.describeRepo?repo=' + BSKY_DID
BSKY_HEAD = RPC + 'sync.getLatestCommit?did=' + BSKY_DID
POST_PREFIX = 'at://' + BSKY_DID + '/app.bsky.feed.post/'
RKEY = re.compile(r'[2-7a-z]{13}')
REWARD = re.compile(r'https://accounts\.klei\.com/link/([A-Za-z0-9_-]{1,160})')
REWARD_IN_TEXT = re.compile(
    r"(?<![\w:/?=%&@#.-])https://accounts\.klei\.com/link/[A-Za-z0-9_-]{1,160}"
    r"(?=$|[\s<>\x22\x27“”‘’，。！？）】]|[.,!;:)\]](?=\s|$))")
IMAGE_LIMIT = 2_100_000


def cid_sha256(value, codec):
    """Accept canonical CIDv1, SHA-256, for raw blobs (0x55) or DAG-CBOR records (0x71)."""
    if not isinstance(value, str) or not re.fullmatch(r'b[a-z2-7]{58}', value):
        return None
    raw = base64.b32decode(value[1:].upper() + '======')
    if (raw[:4] != bytes((1, codec, 0x12, 32))
            or base64.b32encode(raw).decode().rstrip('=').lower() != value[1:]):
        return None
    return raw[4:].hex()


def check_repo_identity(reader):
    identity = json.loads(reader.read('social-identity.json', BSKY_IDENTITY, pinned=True, size_limit=16000))
    if not isinstance(identity, dict) or not isinstance(identity.get('didDoc'), dict):
        raise ValueError('missing official DID document')
    doc = identity['didDoc']
    if (identity.get('did') != BSKY_DID or doc.get('id') != BSKY_DID
            or identity.get('handle') != BSKY_HANDLE or identity.get('handleIsCorrect') is not True
            or 'at://' + BSKY_HANDLE not in doc.get('alsoKnownAs', [])
            or not any(isinstance(service, dict) and service.get('id') in ('#atproto_pds', BSKY_DID + '#atproto_pds')
                       and service.get('type') == 'AtprotoPersonalDataServer'
                       and service.get('serviceEndpoint') == PDS for service in doc.get('service', []))):
        raise ValueError('official Bluesky handle, DID or pinned PDS does not match')


def direct_post_hits(fields):
    """Reconstruct candidate destinations from original UTF-8 facet offsets and the author's own card."""
    text = fields['text']
    encoded = text.encode('utf-8')
    linked_ranges = []
    hits = []

    def add(target, field, start=None, end=None):
        match = REWARD.fullmatch(target) if isinstance(target, str) else None
        if match:
            hit = {'format': 'public_post_link', 'field': field, 'target': target, 'code': match[1]}
            if start is not None:
                hit.update(byte_start=start, byte_end=end)
            hits.append(hit)

    for facet in fields.get('facets', []):
        start, end = facet['index']['byteStart'], facet['index']['byteEnd']
        if type(start) is not int or type(end) is not int or not 0 <= start < end <= len(encoded):
            raise ValueError('invalid official post facet byte range')
        encoded[:start].decode('utf-8')
        encoded[start:end].decode('utf-8')
        linked_ranges.append((start, end))
        for feature in facet['features']:
            if feature.get('$type') == 'app.bsky.richtext.facet#link':
                add(feature.get('uri'), 'facet', start, end)
    for match in REWARD_IN_TEXT.finditer(text):
        start = len(text[:match.start()].encode('utf-8'))
        end = start + len(match[0])
        if not any(left < end and start < right for left, right in linked_ranges):
            add(match[0], 'text', start, end)
    add(fields.get('external_uri'), 'external_uri')
    return hits


def public_post(item):
    uri, record, cid = item['uri'], item['value'], item['cid']
    if (not isinstance(uri, str) or not uri.startswith(POST_PREFIX) or not RKEY.fullmatch(uri[len(POST_PREFIX):])
            or record.get('$type') != 'app.bsky.feed.post' or not cid_sha256(cid, 0x71)):
        raise ValueError('invalid official post URI, type or CID')
    stamp = datetime.fromisoformat(record['createdAt'].replace('Z', '+00:00'))
    if stamp.tzinfo is None:
        raise ValueError('post time missing timezone')
    day = stamp.astimezone(timezone.utc).date()
    embed = record.get('embed', {})
    if embed.get('$type') == 'app.bsky.embed.recordWithMedia':
        embed = embed['media']  # Only the author's attachment. The quoted post belongs to another record.
    kind = embed.get('$type', '')
    images, record_errors = [], []
    if kind == 'app.bsky.embed.images':
        attachments = embed.get('images')
        if not isinstance(attachments, list) or not 1 <= len(attachments) <= 4:
            record_errors.append('invalid attached photo list')
            attachments = []
        for number, attachment in enumerate(attachments, 1):
            try:
                blob = attachment['image']
                image_cid = blob['ref']['$link']
                if (blob.get('$type') != 'blob' or not cid_sha256(image_cid, 0x55)
                        or not isinstance(blob.get('mimeType'), str) or not blob['mimeType'].startswith('image/')):
                    raise ValueError()
                images.append(image_cid)
            except (KeyError, TypeError, ValueError, AttributeError):
                record_errors.append(f'attachment {number} has an invalid photo type or CID')
    fields = {'text': record['text'], 'facets': record.get('facets', [])}
    if kind == 'app.bsky.embed.external':
        external = embed.get('external')
        if isinstance(external, dict) and isinstance(external.get('uri'), str):
            fields['external_uri'] = external['uri']
        else:
            record_errors.append('missing official external card destination')
    post = {'uri': uri, 'cid': cid, 'date': day.isoformat(), 'title': record['text'][:160],
            'media_kind': kind or 'none', 'images': images}
    try:
        if direct_post_hits(fields):
            post['direct_link_fields'] = fields
    except (KeyError, TypeError, ValueError, AttributeError):
        record_errors.append('invalid official post text or facet data')
    if record_errors:
        post['record_errors'] = record_errors
    return post


def list_public_posts(scan, head, state):
    index = state.get('index', {})
    start = scan.since.isoformat()
    if index.get('head') == head and (index['exhausted'] or index['start'] <= start):
        return index
    prior = {(post['uri'], post['cid']): post for post in index.get('posts', [])}
    posts, cursors = {}, set()
    cursor = ''
    exhausted = False
    for _ in range(12):
        url = BSKY_POSTS + ('&cursor=' + cursor if cursor else '')
        response = json.loads(scan.reader.read('social-posts-' + (cursor or 'first') + '.json', url,
                                               pinned=True, size_limit=262144))
        records = response['records']
        if not isinstance(records, list) or len(records) > 12:
            raise ValueError('invalid official post list')
        for item in records:
            try:
                post = prior.get((item['uri'], item['cid'])) or public_post(item)
                posts[post['uri']] = post
            except (KeyError, TypeError, ValueError, AttributeError) as exc:
                scan.error(url, exc)
        cursor = response.get('cursor')
        if not cursor:
            exhausted = True
            break
        if not isinstance(cursor, str) or not RKEY.fullmatch(cursor) or cursor in cursors:
            raise ValueError('invalid or repeated record cursor')
        cursors.add(cursor)
        # listRecords is in reverse record-key order. Klei's TID keys encode creation time.
        alphabet = '234567abcdefghijklmnopqrstuvwxyz'
        tid = 0
        for char in cursor:
            tid = (tid << 5) + alphabet.index(char)
        oldest_key_day = datetime.fromtimestamp((tid >> 10) / 1e6, timezone.utc).date()
        if oldest_key_day < scan.since:
            break
    else:
        scan.error(BSKY_POSTS, 'post pagination limit reached before the requested window')
    index = {'start': start, 'head': head, 'exhausted': exhausted, 'posts': list(posts.values())}
    if not scan.output['errors']:
        state['index'] = index
    return index


def image_result(reader, cid):
    from promo_qr import UnreadableQR, decode_public_qr
    cdn = f'https://cdn.bsky.app/img/feed_fullsize/plain/{BSKY_DID}/{cid}'
    data = reader.read('social-cdn-' + cid, cdn, pinned=True, size_limit=IMAGE_LIMIT, keep_snapshot=False)

    def observation(url, photo, codes):
        return {'url': url, 'sha256': hashlib.sha256(photo).hexdigest(),
                'status': 'decoded' if codes else 'no_qr_detected',
                'rewards': sorted({code for code in codes if REWARD.fullmatch(code)})}

    try:
        return observation(cdn, data, decode_public_qr(data))
    except UnreadableQR as exc:
        preview = observation(cdn, data, exc.decoded)
    url = RPC + f'sync.getBlob?did={BSKY_DID}&cid={cid}'
    try:
        data = reader.read('social-blob-' + cid, url, pinned=True, size_limit=IMAGE_LIMIT, keep_snapshot=False)
        if hashlib.sha256(data).hexdigest() != cid_sha256(cid, 0x55):
            raise ValueError('original photo SHA-256 does not match the official blob CID')
        try:
            codes = decode_public_qr(data)
            if not codes:
                raise ValueError('located QR remains unreadable in the original photo')
            original = observation(url, data, codes)
        except UnreadableQR as exc:
            original = observation(url, data, exc.decoded)
            original.update(status='unreadable', error='one or more located QR codes remain unreadable')
    except (ValueError, OSError, ImportError) as exc:
        preview.update(status='unreadable', error=str(exc)[:300])
        return preview
    if set(preview['rewards']) - set(original['rewards']):
        original['preview'] = preview
    return original


def read_social_promos(scan, state):
    if state.get('version') != 2:
        state.clear()
        state['version'] = 2
    reader = scan.reader
    scan.output['media_scope'] = 'Direct photos in official posts and replies, including the author’s own media on quote posts. Video, previews and quoted attachments are outside this scan.'
    check_repo_identity(reader)
    head = json.loads(reader.read('social-head.json', BSKY_HEAD, pinned=True, size_limit=2048))['cid']
    if not cid_sha256(head, 0x71):
        raise ValueError('invalid official repository revision CID')
    index = list_public_posts(scan, head, state)
    scan.exhausted = index['exhausted']
    images = state.setdefault('images', {})
    if reader.cache and (reader.cache / 'social-qr-results.json').exists():
        images.update(json.loads((reader.cache / 'social-qr-results.json').read_bytes()))
    active = set()
    for post in index['posts']:
        day = date.fromisoformat(post['date'])
        if not scan.includes(day):
            continue
        uri = post['uri']
        url = 'https://bsky.app/profile/' + BSKY_HANDLE + '/post/' + uri[len(POST_PREFIX):]
        fields = post.get('direct_link_fields')
        refs, hits = [], direct_post_hits(fields) if fields else []
        for error in post.get('record_errors', []):
            scan.error(url, error)
        for cid in dict.fromkeys(post['images']):
            active.add(cid)
            outcome = images.get(cid, {})
            if not outcome or outcome.get('retry_on', '9999-12-31') <= scan.until.isoformat():
                failures = outcome.get('failed_attempts', 0)
                prior = outcome
                try:
                    outcome = image_result(reader, cid)
                except (ValueError, OSError, ImportError) as exc:
                    outcome = {**prior, 'status': 'unreadable', 'error': str(exc)[:300]}
                if 'error' in outcome:
                    delay = min(7, 2 ** min(failures, 3))
                    outcome.update(retry_on=(scan.until + timedelta(days=delay)).isoformat(),
                                   failed_attempts=failures + 1)
                images[cid] = outcome
            if 'error' in outcome:
                scan.error(url, cid + ': ' + outcome['error'])
            observations = [outcome]
            if outcome.get('preview'):
                observations.append(outcome['preview'])
            for observed in observations:
                ref = {'cid': cid, 'status': observed['status']}
                if 'url' in observed:
                    ref['url'] = observed['url']
                refs.append(ref)
                for target in observed.get('rewards', []):
                    match = REWARD.fullmatch(target)
                    if match:
                        hits.append({'code': match[1], 'target': target, 'format': 'public_promo_qr',
                                     'image_url': observed['url'], 'image_cid': cid, 'image_sha256': observed['sha256']})
        scan.output['records'].append({'date': post['date'], 'url': url, 'post_uri': uri, 'post_cid': post['cid'],
                                       'author_handle': BSKY_HANDLE, 'author_did': BSKY_DID, 'title': post['title'],
                                       'media_kind': post['media_kind'], 'public_promo_images': refs, 'hits': hits,
                                       **({'direct_link_fields': fields} if fields else {})})
    if not scan.output['errors']:
        state['images'] = {cid: images[cid] for cid in active}
    if reader.snapshot:
        # Preserve replayable public results without retaining the photos or unrelated QR payloads.
        reader.write_snapshot_json('social-qr-results.json', state['images'])
