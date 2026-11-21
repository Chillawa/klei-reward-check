import argparse
import hashlib
import html
import json
import os
import re
import subprocess
import tempfile
import xml.etree.ElementTree as ET
from collections import Counter
from datetime import date, datetime, timedelta, timezone
from email.utils import parsedate_to_datetime
from pathlib import Path
from urllib.parse import parse_qs, urljoin, urlsplit
from zoneinfo import ZoneInfo

from bs4 import BeautifulSoup, NavigableString

from social_promo import BSKY_POSTS, read_social_promos

API = 'https://api.steampowered.com/ISteamNews/GetNewsForApp/v2/?appid=322330&count=100&maxlength=0&feeds=steam_community_announcements'
STEAM_RSS_LANGUAGES = {'steam_rss': 'english', 'steam_rss_schinese': 'schinese', 'steam_rss_tchinese': 'tchinese'}
STEAM_RSS_URL = 'https://store.steampowered.com/feeds/news/app/322330/?l='
FORUM = 'https://kleiforums.com/game-updates/dst/'
FORUM_RSS = 'https://kleiforums.com/rss/6-dont-starve-together-updates.xml/'
FORUM_BOARDS = {
    'developer_log': 'https://kleiforums.com/forums/forum/72-dont-starve-together-developer-log/',
    'general_discussion': 'https://kleiforums.com/forums/forum/66-dont-starve-together-general-discussion/',
}
MAIL = 'https://us2.campaign-archive.com/feed?u=093864427d0ebb0ebe92ec38e&id=e4edaf9994'
MAIL_ARCHIVE = 'https://us2.campaign-archive.com/home/?u=093864427d0ebb0ebe92ec38e&id=e4edaf9994'
SOURCES = {**{name: STEAM_RSS_URL + language for name, language in STEAM_RSS_LANGUAGES.items()},
           'steam_api': API, 'newsletter': MAIL,
           'newsletter_archive': MAIL_ARCHIVE, 'klei_social_promo': BSKY_POSTS,
           'klei_pc_versions': FORUM, 'klei_forum_topics': FORUM_BOARDS['general_discussion']}
STORE = 'https://store.steampowered.com/news/app/322330/view/'
OLDER_EVENTS = {'1835236783561228': STORE + '669493247455790460', '1836506165551438': STORE + '716782947012706691'}
FORUM_HOSTS = {'kleiforums.com', 'forums.kleientertainment.com'}
STAFF_GROUPS = {'developer', 'klei developer', 'klei entertainment', 'klei staff'}
REWARD_PATH = re.compile(r'/link/([A-Za-z0-9_-]+)/?')
REWARD_TEXT = re.compile(r'''(?<![\w:/?=&])https?://accounts\.klei\.com/link/[A-Za-z0-9_-]+/?(?=$|[\s?#<>"'\[\](){},!;，。！？、）]|\.(?:\s|$))''', re.I)
BB_URL = re.compile(r'\[url(?:=(?:"([^"]+)"|([^\]]+)))?\](.*?)\[/url\]', re.I | re.S)
READ_ERRORS = (OSError, ValueError, KeyError, TypeError, IndexError, ET.ParseError, subprocess.SubprocessError)


def atomic_write(path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as output:
        temp = Path(output.name)
        try:
            output.write(data)
            output.close()
            os.replace(temp, path)
        finally:
            temp.unlink(missing_ok=True)


class PublicInputs:
    def __init__(self, cache=None, snapshot=None):
        self.cache = cache
        self.snapshot = snapshot
        self.mail_items = None

    def read(self, key, url, *, mail_hop=False, pinned=False, size_limit=12582912, keep_snapshot=True):
        if self.cache:
            failed = self.cache / (key + '.error')
            if failed.exists():
                raise ValueError(failed.read_text())
            path = self.cache / key
            if path.stat().st_size > size_limit:
                raise ValueError("input exceeds byte limit")
            return path.read_bytes()
        # Archive short links can redirect. Inspect each destination before visiting it.
        redirect_options = ['-w', '\n%{response_code}\n%{redirect_url}'] if mail_hop else ['-L']
        if pinned:
            redirect_options = []
        request = subprocess.run(['curl', '-q', '-fsS', *redirect_options, '--compressed', '--proto', '=https',
                                  '--proto-redir', '=https', '--max-time', '20', '--retry', '1',
                                  '--max-filesize', str(size_limit), url], capture_output=True,
                                 creationflags=getattr(subprocess, 'CREATE_NO_WINDOW', 0))
        if request.returncode:
            detail = request.stderr.decode(errors='replace').strip()[:400]
            if self.snapshot and keep_snapshot:
                atomic_write(self.snapshot / (key + '.error'), detail.encode())
            raise ValueError(detail or f'curl exit {request.returncode}')
        if self.snapshot and keep_snapshot:
            atomic_write(self.snapshot / key, request.stdout)
            (self.snapshot / (key + '.error')).unlink(missing_ok=True)
        return request.stdout

    def write_snapshot_json(self, key, value):
        atomic_write(self.snapshot / key, json.dumps(value, ensure_ascii=False).encode())

    def newsletter_items(self):
        if self.mail_items is None:
            self.mail_items = ET.fromstring(self.read('mailfeed', MAIL)).findall('./channel/item')
        return self.mail_items


def reward_url(value):
    parsed = urlsplit(html.unescape(value).strip())
    if parsed.hostname == 'steamcommunity.com' and parsed.path.rstrip('/') == '/linkfilter':
        parsed = urlsplit(parse_qs(parsed.query).get('u', [''])[0])
    if parsed.scheme in ('http', 'https') and parsed.netloc.lower() == 'accounts.klei.com':
        return REWARD_PATH.fullmatch(parsed.path)


def linked_reward(url, label, image=None):
    try:
        match = reward_url(url)
    except ValueError:
        return None
    if match:
        result = {'code': match[1], 'label': label, 'target': 'https://accounts.klei.com/link/' + match[1]}
        if image is not None:
            result['image'] = image
        return result


def text_rewards(value):
    hits = []
    for match in REWARD_TEXT.finditer(value):
        hit = linked_reward(match[0], match[0])
        if hit:
            hits.append(dict(hit, format='plain_text'))
    return hits


def html_rewards(body):
    found = []
    for a in body.select('a[href]'):
        pic = a.find('img')
        img = {'alt': pic.get('alt', ''), 'src': pic.get('src', '')} if pic else None
        hit = linked_reward(a['href'], a.get_text(strip=True), img)
        if hit:
            block = a.find_parent(['p', 'li'])
            if block is not None and len(block.select('a[href]')) == 1:
                line = ' '.join(block.stripped_strings)
                if line and len(line) <= 240:
                    hit['link_sentence'] = line
            found.append(dict(hit, format='html_href'))
    # Anchor labels can mention a different destination. Extract unlinked text only.
    plain = ' '.join(str(s) for s in body.find_all(string=True)
                     if type(s) is NavigableString and s.strip() and not s.find_parent(['a', 'style', 'script']))
    found.extend(text_rewards(plain))
    return found


def bb_rewards(body):
    found = []
    for quoted, unquoted, label in BB_URL.findall(html.unescape(body)):
        hit = linked_reward(quoted or unquoted or label, label)
        if hit:
            found.append(dict(hit, format='bbcode_url'))
    found.extend(text_rewards(BB_URL.sub('', body)))
    return found


def official_link(url, kind):
    parsed = urlsplit(url)
    if parsed.scheme != 'https' or parsed.netloc != parsed.hostname:
        return False
    if kind in ('newsletter', 'newsletter_archive'):
        if parsed.fragment or re.search(r'[\s\\\x00-\x1f\x7f]', url):
            return False
        if parsed.hostname == 'mailchi.mp':
            return not parsed.query and bool(re.fullmatch(r'/kleientertainment/[a-zA-Z0-9_-]+/?', parsed.path))
        query = parse_qs(parsed.query, keep_blank_values=True)
        return (parsed.hostname in {'us2.campaign-archive.com', 'kleientertainment.us2.campaign-archive.com'}
                and parsed.path == '/' and set(query) == {'u', 'id'} and query['u'] == ['093864427d0ebb0ebe92ec38e']
                and len(query['id']) == 1 and bool(re.fullmatch(r'[a-fA-F0-9]{10}', query['id'][0])))
    if kind in STEAM_RSS_LANGUAGES:
        if (parsed.fragment or re.search(r'[\s\\\x00-\x1f\x7f]', url)
                or parsed.query not in ('', 'l=' + STEAM_RSS_LANGUAGES[kind])):
            return False
        return bool(parsed.hostname == 'store.steampowered.com' and re.fullmatch(r'/news/app/322330/view/[0-9]+/?', parsed.path)
                    or parsed.hostname == 'steamcommunity.com' and re.fullmatch(r'/(?:games|ogg)/322330/announcements/detail/[0-9]+/?', parsed.path))
    if parsed.hostname not in FORUM_HOSTS:
        return False
    path = r'/game-updates/dst/[\w-]+/?' if kind == 'klei_pc_versions' else r'/forums/topic/\d+[-\w%]*/?'
    return bool(re.fullmatch(path, parsed.path, re.I))


def iso_day(value):
    stamp = datetime.fromisoformat(value.replace('Z', '+00:00'))
    if stamp.tzinfo is None:
        raise ValueError('missing timezone in forum timestamp')
    return stamp.astimezone(timezone.utc).date()


def article_key(group, url):
    return group + '-' + hashlib.sha256(url.encode()).hexdigest()[:24]


class Scan:
    def __init__(self, reader, since, until, name):
        self.reader, self.since, self.until = reader, since, until
        self.dates = []
        self.output = {'index': SOURCES[name], 'records': [], 'errors': []}
        self.exhausted = False

    def includes(self, day):
        self.dates.append(day)
        return self.since <= day <= self.until

    def record(self, day, url, hits, body, **fields):
        digest = hashlib.sha256(body.encode() if isinstance(body, str) else body).hexdigest()
        self.output['records'].append(dict(date=day.isoformat(), url=url, hits=hits, body_sha256=digest, **fields))

    def error(self, url, exc):
        self.output['errors'].append({'url': url, 'message': str(exc)[:400]})

    def finish(self):
        out, dates = self.output, self.dates
        reaches = self.exhausted or bool(dates and min(dates) < self.since)
        if 'boards' in out:
            reaches = all(board['index_reaches_start'] for board in out['boards'].values())
        out.update(sampled_list_items=len(dates), sample_range=[min(dates).isoformat(), max(dates).isoformat()] if dates else [],
                   index_reaches_start=reaches, in_window_count=len(out['records']))
        if out['errors']:
            out['status'] = 'partial' if out['records'] or dates else 'failed'
        else:
            out['status'] = 'ok' if reaches else 'limited_history'
        return out


def read_feed(scan, name, steam_links):
    url = SOURCES[name]
    language = STEAM_RSS_LANGUAGES.get(name)
    if name == 'newsletter':
        items = scan.reader.newsletter_items()
    else:
        key = 'rss' if name == 'steam_rss' else 'rss-' + language
        items = ET.fromstring(scan.reader.read(key, url)).findall('./channel/item')
    if not items:
        raise ValueError('RSS contains no items')
    for item in items:
        link = item.findtext('link') or ''
        try:
            stamp = parsedate_to_datetime(item.findtext('pubDate'))
            if not scan.includes(stamp.astimezone(timezone.utc).date()):
                continue
            if not official_link(link, name):
                raise ValueError('unexpected article URL for official feed')
            title = item.findtext('title')
            fields = {}
            if name == 'steam_rss':
                link = urlsplit(link)._replace(query='').geturl()
                steam_links[(title, int(stamp.timestamp()))] = link
            elif language:
                # Steam supplies a language-neutral permalink alongside this edition's full body.
                link = urlsplit(link)._replace(query='l=' + language).geturl()
                fields['feed_language'] = language
            body = item.findtext('{http://purl.org/rss/1.0/modules/content/}encoded') or item.findtext('description')
            if not body:
                raise ValueError('missing full feed body')
            day = stamp.astimezone(timezone.utc).date()
            scan.record(day, link, html_rewards(BeautifulSoup(body, 'html.parser')), body, title=title, **fields)
        except READ_ERRORS as exc:
            scan.error(link or url, exc)


def archive_destination(url):
    parsed = urlsplit(url)
    target = parsed._replace(scheme='https').geturl()
    if (parsed.scheme in ('https', 'http') and not re.search(r'[\s\\]', url)
            and (official_link(target, 'newsletter_archive')
                 or re.fullmatch(r'https://eepurl\.com/[a-zA-Z0-9_-]+/?', target))):
        return target
    raise ValueError('unexpected campaign archive destination')


def read_archived_campaign(reader, url):
    visited = set()
    while url not in visited and len(visited) < 6:
        visited.add(url)
        raw = reader.read(article_key('mail-hop', url), url, mail_hop=True)
        html_body, status, location = raw.rsplit(b'\n', 2)
        if status in (b'301', b'302', b'303', b'307', b'308') and location:
            url = archive_destination(urljoin(url, location.decode('utf-8')))
            continue
        if status != b'200' or not official_link(url, 'newsletter_archive'):
            raise ValueError('archive link did not resolve to a public Klei campaign')
        doc = BeautifulSoup(html_body, 'html.parser')
        body = doc.select_one('#bodyTable, #templateContainer')
        if not body:
            raise ValueError('missing campaign email body')
        canonical = doc.select_one('input#clipboardSource[value]')
        if canonical:
            url = canonical['value']
            if not official_link(url, 'newsletter_archive'):
                raise ValueError('unexpected canonical campaign URL')
        return url, body, doc.title.get_text(strip=True) if doc.title else None
    raise ValueError('campaign redirect loop or limit reached')


def mail_feed_matches(reader):
    # Mailchimp prints the publisher's local calendar day on the archive list.
    # Duplicate subjects can be resends. Require a unique dated edition on both
    # lists, a complete RSS body and the same canonical URL inside the email.
    editions = {}
    for item in reader.newsletter_items():
        try:
            stamp = parsedate_to_datetime(item.findtext('pubDate'))
            if stamp.tzinfo is None:
                continue
            title = item.findtext('title')
            key = (stamp.astimezone(ZoneInfo('America/Vancouver')).date(), title)
            url = item.findtext('link') or ''
            raw = item.findtext('{http://purl.org/rss/1.0/modules/content/}encoded') or item.findtext('description')
            body = BeautifulSoup(raw or '', 'html.parser')
            email = body.select_one('#bodyTable, #templateContainer')
            if not (email and official_link(url, 'newsletter') and body.title and body.title.get_text(strip=True) == title
                    and any(a['href'] in (url, url + '?e=[UNIQID]') for a in email.select('a[href]'))):
                editions[key] = None
                continue
            editions[key] = None if key in editions else (url, email)
        except READ_ERRORS:
            continue
    return editions


def read_newsletter_archive(scan, newsletter_permalinks=None):
    reader = scan.reader
    if newsletter_permalinks is None and reader.cache and (reader.cache / 'mail-permalinks.json').exists():
        newsletter_permalinks = json.loads((reader.cache / 'mail-permalinks.json').read_bytes())
    if newsletter_permalinks is None:
        newsletter_permalinks = {}
    if not isinstance(newsletter_permalinks, dict):
        raise ValueError('invalid retained newsletter permalink map')
    if reader.snapshot:
        reader.write_snapshot_json('mail-permalinks.json', newsletter_permalinks)
    index = BeautifulSoup(scan.reader.read('mail-archive', MAIL_ARCHIVE), 'html.parser')
    campaigns = index.select('#archive-list li.campaign')
    if not campaigns:
        raise ValueError('no campaign archive rows')
    listings = scan.output['listings'] = []
    rows = []
    for campaign in campaigns:
        try:
            day_text = re.match(r'(\d{2}/\d{2}/\d{4})\s+-\s+', campaign.get_text(' ', strip=True))
            if not day_text:
                raise ValueError('missing printed campaign date')
            day = datetime.strptime(day_text[1], '%m/%d/%Y').date()
            if not scan.includes(day):
                continue
            link = campaign.select_one('a[href]')
            if not link or not link.get_text(strip=True):
                raise ValueError('missing campaign link or title')
            url = archive_destination(link['href'])
            title = link.get_text(' ', strip=True)
            listings.append({'date': day.isoformat(), 'title': title, 'url': url})
            rows.append((day, title, url))
        except READ_ERRORS as exc:
            scan.error(MAIL_ARCHIVE, exc)
    if not rows:
        return
    try:
        editions = mail_feed_matches(scan.reader)
    except READ_ERRORS:
        # The archive can still discover articles independently of the RSS feed.
        editions = {}
    occurrences = Counter((day, title) for day, title, url in rows)
    archive_links = Counter(url for day, title, url in rows)
    for day, title, url in rows:
        try:
            retained_url = None
            saved = newsletter_permalinks.get(url)
            if (isinstance(saved, dict) and saved.get('title') == title and saved.get('date') == day.isoformat()
                    and isinstance(saved.get('url'), str) and official_link(saved['url'], 'newsletter_archive')
                    and archive_links[url] == 1):
                retained_url = saved['url']
            match = editions.get((day, title)) if occurrences[day, title] == 1 and archive_links[url] == 1 else None
            if match and (not official_link(url, 'newsletter_archive') or url == match[0]):
                canonical, body = match
                body_source = MAIL
                if retained_url and retained_url != canonical:
                    raise ValueError('retained permalink conflicts with the current RSS edition')
                permalink_basis = 'rss_edition_match'
            elif retained_url:
                canonical, body, page_title = read_archived_campaign(reader, retained_url)
                if canonical != retained_url or page_title != title:
                    raise ValueError('retained permalink returned a different campaign')
                body_source = retained_url
                permalink_basis = 'retained_archive_permalink'
            else:
                canonical, body, page_title = read_archived_campaign(reader, url)
                if page_title and page_title != title:
                    raise ValueError('archive link returned a different campaign title')
                body_source = canonical
                permalink_basis = 'archive_link' if page_title == title and archive_links[url] == 1 else None
            scan.record(day, canonical, html_rewards(body), str(body), title=title, archive_url=url,
                        body_source=body_source, permalink_basis=permalink_basis, date_basis='printed campaign archive date')
        except READ_ERRORS as exc:
            scan.error(url, exc)


def read_steam_api(scan, steam_links):
    seen = set()
    cursor = None
    while True:
        url = API if cursor is None else API + '&enddate=' + str(cursor)
        data = json.loads(scan.reader.read('api' if cursor is None else f'api-{cursor}', url))['appnews']
        if data['appid'] != 322330:
            raise ValueError('unexpected app ID in Steam feed')
        items = data['newsitems']
        if not items and cursor is None:
            raise ValueError('empty Steam feed')
        for item in items:
            gid = str(item['gid'])
            if gid in seen:
                continue
            seen.add(gid)
            day = datetime.fromtimestamp(item['date'], timezone.utc).date()
            if not scan.includes(day):
                continue
            if item['appid'] != 322330 or item['feedname'] != 'steam_community_announcements' or not gid.isdigit():
                scan.error(url, 'unexpected Steam article provenance')
                continue
            body = item['contents']
            if not isinstance(body, str) or not body.strip():
                scan.error(url, f'missing article body, gid {gid}')
                continue
            link = (steam_links.get((item['title'], item['date'])) or OLDER_EVENTS.get(gid)
                    or 'https://store.steampowered.com/news/externalpost/steam_community_announcements/' + gid)
            scan.record(day, link, bb_rewards(body), body, title=item['title'], gid=gid,
                        appid=item['appid'], feedname=item['feedname'])
        if len(items) < 100:
            scan.exhausted = True
            break
        oldest = min(item['date'] for item in items)
        if datetime.fromtimestamp(oldest, timezone.utc).date() < scan.since:
            break
        # Include the boundary second on the following page and deduplicate by gid.
        next_cursor = oldest + 1
        if cursor is not None and next_cursor >= cursor:
            scan.error(url, 'Steam pagination did not advance past boundary timestamp')
            break
        cursor = next_cursor


def read_versions(scan):
    seen = set()
    kinds = Counter()
    scan.output['release_types'] = kinds
    for page in range(1, 101):
        url = FORUM if page == 1 else FORUM + f'page/{page}/?d=7'
        try:
            index = BeautifulSoup(scan.reader.read('forum' if page == 1 else f'p{page}', url), 'html.parser')
            rows = index.select('a.cRelease')
            if not rows:
                raise ValueError('release index contains no version rows')
            dates = []
            for a in rows:
                line = re.search(r'(\d+)\s+(Release|Test)\s+Released\s+(\d+/\d+/\d+)', a.get_text(' ', strip=True))
                if not line:
                    raise ValueError('unrecognized release date or type')
                build, kind, stamp = line.groups()
                day = datetime.strptime(stamp, '%m/%d/%y').date()
                dates.append(day)
                target = urljoin(url, a['href'])
                if target in seen:
                    continue
                seen.add(target)
                if not scan.includes(day):
                    continue
                try:
                    if not official_link(target, 'klei_pc_versions'):
                        raise ValueError('unexpected release URL')
                    doc = BeautifulSoup(scan.reader.read(urlsplit(target).path.rstrip('/').split('/')[-1], target), 'html.parser')
                    sections = doc.select('section.ipsType_richText')
                    if not sections:
                        raise ValueError('no official update body sections')
                    discussion = doc.find('a', string=lambda s: s and s.strip() == 'Discussion Topic')
                    hits = [hit for section in sections for hit in html_rewards(section)]
                    scan.record(day, target, hits, ''.join(map(str, sections)), build=build, type=kind,
                                discussion_url=discussion['href'] if discussion else None)
                    kinds[kind] += 1
                except READ_ERRORS as exc:
                    scan.error(target, exc)
            if min(dates) < scan.since:
                break
            if not index.select_one('.ipsPagination_next:not(.ipsPagination_inactive) a'):
                scan.exhausted = True
                break
        except READ_ERRORS as exc:
            scan.error(url, exc)
            break
    else:
        scan.error(FORUM, 'release pagination limit reached')
    if scan.output['errors']:
        read_versions_feed(scan)


def read_versions_feed(scan):
    # The official cross-platform RSS can supply PC articles when the ordinary
    # release index or an individual release page is unavailable.
    known = {row['url'] for row in scan.output['records']}
    scan.output['fallback_index'] = FORUM_RSS
    try:
        items = ET.fromstring(scan.reader.read('forum-rss', FORUM_RSS)).findall('./channel/item')
        if not items:
            raise ValueError('empty release RSS')
        for item in items:
            target = item.findtext('link') or ''
            if target in known or not official_link(target, 'klei_pc_versions'):
                continue
            day = parsedate_to_datetime(item.findtext('pubDate')).astimezone(timezone.utc).date()
            if not scan.includes(day):
                continue
            body = item.findtext('description')
            if not body:
                scan.error(target, 'missing full release RSS body')
                continue
            scan.record(day, target, html_rewards(BeautifulSoup(body, 'html.parser')), body,
                        build=item.findtext('title'), date_basis='UTC release RSS publication date',
                        publication_feed=FORUM_RSS)
            known.add(target)
    except READ_ERRORS as exc:
        scan.error(FORUM_RSS, exc)


def read_staff_post(scan, target, title, board, day):
    doc = BeautifulSoup(scan.reader.read(article_key('topic', target), target), 'html.parser')
    post = doc.select_one('article.cPost, article.ipsComment')
    if not post:
        raise ValueError('missing opening forum post')
    posted = post.select_one('.ipsComment_meta time[datetime]') or post.select_one('time[datetime]')
    if not posted or iso_day(posted['datetime']) != day:
        raise ValueError('opening post date does not match topic creation date')
    author = post.select_one('.cAuthorPane, .ipsComment_author')
    if not author:
        raise ValueError('opening post author metadata missing')
    groups = [el.get_text(' ', strip=True) for el in author.select('li.ipsType_break, .cAuthorPane_info > li, .ipsType_light, .ipsType_normal')]
    group = next((g for g in groups if g.casefold() in STAFF_GROUPS), None)
    if not group:
        if not groups:
            raise ValueError('opening post has no recognizable member-group metadata')
        return
    body = post.select_one('[data-role="commentContent"], .ipsComment_content .ipsType_richText')
    if not body:
        raise ValueError('opening post text missing')
    for quote in body.select('blockquote.ipsQuote, [data-ipsquote]'):
        if quote.parent:
            quote.decompose()
    scan.record(day, target, html_rewards(body), str(body), title=title, staff_group=group, board=board)


def read_forum_topics(scan):
    boards = scan.output['boards'] = {}
    checked = set()
    for board, base in FORUM_BOARDS.items():
        page_limit = 2
        coverage = boards[board] = {'index': base, 'index_reaches_start': False, 'page_limit': page_limit}
        for page in range(1, page_limit + 1):
            url = base + (f'page/{page}/' if page > 1 else '') + '?sortby=start_date&sortdirection=desc'
            try:
                doc = BeautifulSoup(scan.reader.read(f'{board}-{page}', url), 'html.parser')
                rows = doc.select('li.ipsDataItem')
                if not rows:
                    raise ValueError('no forum topic rows')
                regular_dates = []
                for row in rows:
                    a = row.select_one('.ipsDataItem_title a[data-role="title"], .ipsDataItem_title a[href*="/forums/topic/"]')
                    if not a:
                        continue
                    target = urljoin(base, a['href']).split('?', 1)[0].split('#', 1)[0]
                    if not official_link(target, 'klei_forum_topics'):
                        raise ValueError('unexpected topic URL')
                    stamp = row.select_one('.ipsDataItem_main .ipsDataItem_meta time[datetime], .ipsDataItem_main time[datetime]')
                    if not stamp:
                        raise ValueError('missing topic creation timestamp')
                    day = iso_day(stamp['datetime'])
                    if not row.select_one('.fa-thumb-tack, .fa-thumbtack, [data-ipsTooltip][title*="Pinned"]'):
                        regular_dates.append(day)
                    if not scan.includes(day) or target in checked:
                        continue
                    checked.add(target)
                    try:
                        read_staff_post(scan, target, a.get_text(' ', strip=True), board, day)
                    except READ_ERRORS as exc:
                        scan.error(target, exc)
                if not regular_dates:
                    raise ValueError('no dated non-pinned topics in forum index')
                if (min(regular_dates) < scan.since or
                        not doc.select_one('.ipsPagination_next:not(.ipsPagination_inactive) a')):
                    coverage['index_reaches_start'] = True
                    break
            except READ_ERRORS as exc:
                scan.error(url, exc)
                break


def check_sources(cache=None, since=None, until=None, *, lookback_days=30, selected=None, snapshot=None, source_starts=None,
                  newsletter_permalinks=None, social_cache=None):
    until = until or datetime.now(timezone.utc).date()
    since = since or until - timedelta(days=lookback_days - 1)
    if lookback_days < 1 or since > until:
        raise ValueError('invalid date window')
    sources, candidates, steam_links = {}, {}, {}
    reader = PublicInputs(cache, snapshot)
    for name in SOURCES:
        if selected and name not in selected:
            continue
        start = since if source_starts is None else source_starts.get(name, since)
        if not since <= start <= until:
            raise ValueError(f'invalid date window for {name}')
        scan = Scan(reader, start, until, name)
        scan.output['start'] = start.isoformat()
        try:
            if name in STEAM_RSS_LANGUAGES or name == 'newsletter':
                read_feed(scan, name, steam_links)
            elif name == 'newsletter_archive':
                read_newsletter_archive(scan, newsletter_permalinks)
            elif name == 'klei_social_promo':
                read_social_promos(scan, social_cache if social_cache is not None else {})
            elif name == 'steam_api':
                read_steam_api(scan, steam_links)
            elif name == 'klei_pc_versions':
                read_versions(scan)
            else:
                read_forum_topics(scan)
        except READ_ERRORS as exc:
            scan.error(SOURCES[name], exc)
        sources[name] = scan.finish()
        for row in scan.output['records']:
            for code, target in {(h['code'], h['target']) for h in row['hits']}:
                candidate = candidates.setdefault(code, {'code': code, 'target': target, 'evidence': []})
                candidate['evidence'].append({'source': name, 'url': row['url'], 'date': row['date'],
                                              'title': row.get('title') or row.get('build')})
    return {'start': since.isoformat(), 'end_inclusive': until.isoformat(),
            'scanned_at_utc': datetime.now(timezone.utc).isoformat(timespec='seconds'),
            'source_date_basis': 'UTC publication dates, printed calendar day for Klei PC release and newsletter archive indexes',
            'mode': 'cache' if cache else 'live', 'sources': sources,
            'codes_by_source': {c: sorted({e['source'] for e in candidates[c]['evidence']}) for c in sorted(candidates)},
            'candidates': [candidates[c] for c in sorted(candidates)]}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument('--from-cache', type=Path, metavar='DIRECTORY')
    mode.add_argument('--live', action='store_true', help='fetch public sources (default)')
    parser.add_argument('--snapshot-dir', type=Path, help='save public source inputs and fetch errors for offline replay')
    parser.add_argument('--since', type=date.fromisoformat, help='first date, YYYY-MM-DD')
    parser.add_argument('--until', type=date.fromisoformat, help='last date, YYYY-MM-DD (UTC today by default)')
    parser.add_argument('--lookback-days', type=int, default=30, help='inclusive window size when --since is omitted')
    parser.add_argument('--source', choices=SOURCES, action='append', help='scan only this source (repeatable)')
    parser.add_argument('--output', type=Path, help='atomically write JSON to this file (default stdout)')
    args = parser.parse_args()
    if args.from_cache and args.snapshot_dir:
        parser.error('--from-cache and --snapshot-dir are mutually exclusive')
    try:
        result = check_sources(args.from_cache, args.since, args.until, lookback_days=args.lookback_days,
                               selected=args.source, snapshot=args.snapshot_dir)
    except ValueError as exc:
        parser.error(str(exc))
    data = json.dumps(result, indent=2, ensure_ascii=False)
    if args.output:
        atomic_write(args.output, data.encode())
    else:
        print(data)
    raise SystemExit(int(any(s['status'] != 'ok' for s in result['sources'].values())))
