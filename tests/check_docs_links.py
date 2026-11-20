#!/usr/bin/env python3
"""Check repo-internal relative links in the website-synced Markdown docs.

Guards against the #412 regression class: docs get renamed or moved and the
relative links pointing at them silently 404 — on GitHub and on the website
docs center, which syncs docs/{zh,en}/ from this repo.

Deliberately repo-local only (per #412): http(s)/mailto targets and anchor
fragments are skipped — no network, no fragment validation. Also enforces
zh/en bilingual parity: every docs/zh/*.md needs a docs/en/ counterpart and
vice versa (deployment-faq.md was the one file that regressed).

Run from the repo root: python3 scripts/check-docs-links.py
"""
import os
import posixpath
import re
import subprocess
import sys
from urllib.parse import unquote

# Same scope the website syncs, plus the repo-facing docs. AGENTS.md files
# are gitignored and excluded implicitly by going through git ls-files.
ROOT_DOCS = {'README.md', 'README.zh.md', 'CONTRIBUTING.md'}

LINK = re.compile(r'(!?\[[^\]]*\]\()([^)\r\n]+)(\))')
SCHEME = re.compile(r'^[a-zA-Z][a-zA-Z0-9+.-]*:')


def doc_files():
    out = subprocess.run(['git', 'ls-files', '*.md'],
                         capture_output=True, text=True, check=True).stdout
    # ls-files reflects the index: filter out locally deleted-but-unstaged
    # files so the parity check reports them instead of open() crashing.
    return [p for p in out.splitlines()
            if os.path.exists(p) and (p.startswith('docs/') or p in ROOT_DOCS)]


def dest_of(raw):
    """Link destination without a possible "title" or <angle-bracket> form."""
    dest = raw.strip()
    if dest.startswith('<'):
        m = re.match(r'<([^>]*)>', dest)
        dest = m.group(1) if m else dest[1:]
    return re.split(r'\s+"', dest)[0]


def check_links(files):
    errors = []
    for path in files:
        text = open(path, encoding='utf-8', errors='replace').read()
        for m in LINK.finditer(text):
            dest = dest_of(m.group(2))
            if not dest or dest.startswith(('#', '/', '//')) or SCHEME.match(dest):
                continue
            target = dest.split('#')[0].strip()
            if not target:
                continue  # pure fragment
            resolved = posixpath.normpath(
                posixpath.join(posixpath.dirname(path), unquote(target)))
            if not os.path.exists(resolved):
                line = text[:m.start()].count('\n') + 1
                errors.append(f'{path}:{line}: broken relative link -> {dest} '
                              f'(resolved {resolved})')
    return errors


def check_parity(files):
    strip = lambda p: p[len('docs/zh/'):] if p.startswith('docs/zh/') else p[len('docs/en/'):]
    zh = {strip(p) for p in files if p.startswith('docs/zh/')}
    en = {strip(p) for p in files if p.startswith('docs/en/')}
    return ([f'zh-only (missing docs/en/{f})' for f in sorted(zh - en)]
            + [f'en-only (missing docs/zh/{f})' for f in sorted(en - zh)])


def main():
    files = doc_files()
    errors = check_links(files) + check_parity(files)
    if errors:
        print(f'\n{len(errors)} problem(s):')
        for e in errors:
            print(f'  {e}')
        return 1
    print(f'OK: {len(files)} docs checked — relative links resolve, zh/en parity holds')
    return 0


if __name__ == '__main__':
    sys.exit(main())
