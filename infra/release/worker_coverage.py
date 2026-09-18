#!/usr/bin/env python3
"""A clean scan without actual Debian package coverage is not acceptance."""
import json
import sys

report = json.load(open(sys.argv[1]))
if report.get('Metadata', {}).get('OS', {}).get('Family') != 'debian':
    raise SystemExit('Worker Debian OS was not detected; security coverage incomplete.')
if not any(result.get('Class') == 'os-pkgs' and result.get('Type') == 'debian'
           for result in report.get('Results', [])):
    raise SystemExit('Worker Debian package scan missing; security coverage incomplete.')
